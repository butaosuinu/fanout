package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
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
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/lifecycle"
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
	formatExplicit     bool
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
	if cfg.actionMode() {
		if missing := checkPlanActionDeps(cfg); len(missing) > 0 {
			lg.Err("missing dependencies:")
			for _, d := range missing {
				fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
			}
			return exitcode.Env
		}
		return cmdPlanLifecycle(cfg, lg)
	}

	if missing := checkPlanDeps(); len(missing) > 0 {
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
	hookConfig := hooks.LoadUserConfig(lg)

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
	copyLivePlanSpec := func() exitcode.Code {
		if cfg.DryRun {
			return exitcode.OK
		}
		if err := copyPlanSpec(cfg.SpecPath, rt.info.ProjectRoot, spec.Plan.Slug); err != nil {
			lg.Err("copy plan spec: %v", err)
			return exitcode.Env
		}
		return exitcode.OK
	}
	cfg.SpecArg = planRerunSpecArg(cfg, spec)

	parentRef := planParentRef(spec.Plan.Slug)
	fanned := mergeTaskFanned(store.FannedTaskIDsForParent(parentRef), existingPlanWorktreeFanned(rt.info.ProjectRoot, spec))
	plan := buildTaskPlan(cfg, spec, fanned, func(task planspec.Task) bool {
		return planTaskComplete(rt.gh, cliCfg, rt.info.ProjectRoot, store, spec, task, lg)
	})
	logTaskPlanDetails(plan, lg)

	if plan.AfterFilter == 0 {
		if code := copyLivePlanSpec(); code != exitcode.OK {
			return code
		}
		lg.Info("all plan tasks filtered out by --only/--skip. nothing to do.")
		return exitcode.OK
	}
	if plan.UnfannedCount == 0 {
		if code := copyLivePlanSpec(); code != exitcode.OK {
			return code
		}
		if len(plan.AlreadyComplete) == 0 {
			lg.Ok("all %d plan task(s) already have a fanout pane. nothing to do.", len(plan.AlreadyFanned))
		} else {
			lg.Ok("all %d selected plan task(s) already have a fanout pane or are complete. nothing to do.", plan.AfterFilter)
		}
		return exitcode.OK
	}
	if err := validateTaskAgents(cliCfg, plan.Targets, plan.LimitDeferred); err != nil {
		lg.Err("%s", err.Error())
		return exitcode.Env
	}
	if code := copyLivePlanSpec(); code != exitcode.OK {
		return code
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
		teamCtx = buildTaskTeamContext(rt.info.ProjectRoot, parentRef, plan.Targets)
	}

	result := executeTaskPlan(cliCfg, lg, rt.info, spec, plan.Targets, resolvedSettings, hookConfig, recorder, c, commandName, teamCtx)
	printTaskSummary(plan, result, cfg, lg, c, commandName)

	if !cfg.DryRun && result.Created > 0 {
		bindDashboardKey(lg, resolvedSettings.DashboardKeybind)
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

	if result.Failed > 0 {
		return exitcode.Env
	}
	return exitcode.OK
}

func parsePlanCommand(args []string, lg *log.Logger) (planCommandConfig, exitcode.Code) {
	cfg := planCommandConfig{SleepBetween: cliflags.DefaultSleepBetween, Format: cliflags.DefaultFormat}
	var limitRaw, sleepRaw string
	var rawAgents []string

	valueOptions := map[string]func(string){
		"--agent":         func(v string) { rawAgents = append(rawAgents, v) },
		"--base-branch":   func(v string) { cfg.BaseBranch = v },
		"--branch-prefix": func(v string) { cfg.BranchPrefix = v },
		"--close":         func(v string) { cfg.CloseTaskID = v },
		"--merge":         func(v string) { cfg.MergeTaskID = v },
		"--limit":         func(v string) { limitRaw = v },
		"--only":          func(v string) { cfg.OnlyArg = v },
		"--skip":          func(v string) { cfg.SkipArg = v },
		"--session":       func(v string) { cfg.Session = v },
		"--format": func(v string) {
			cfg.Format = v
			cfg.formatExplicit = true
		},
		"--sleep": func(v string) { sleepRaw = v },
	}
	boolOptions := map[string]func(){
		"--dry-run":                 func() { cfg.DryRun = true },
		"--debug":                   func() { cfg.Debug = true },
		"--no-refresh":              func() { cfg.NoRefresh = true },
		"--unblocked-only":          func() { cfg.UnblockedOnly = true },
		"--team":                    func() { cfg.Team = true },
		"--status":                  func() { cfg.StatusMode = true },
		"--cleanup":                 func() { cfg.CleanupMode = true },
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
	if code := validatePlanActionFlags(cfg, limitRaw, sleepRaw, len(rawAgents) > 0, lg); code != exitcode.OK {
		return planCommandConfig{}, code
	}
	for _, raw := range rawAgents {
		if err := parsePlanAgentArg(&cfg, raw); err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
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
	if cfg.CloseTaskID != "" {
		if err := validatePlanTaskID("--close", cfg.CloseTaskID); err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
	}
	if cfg.MergeTaskID != "" {
		if err := validatePlanTaskID("--merge", cfg.MergeTaskID); err != nil {
			lg.Err("%s", err.Error())
			return planCommandConfig{}, exitcode.Env
		}
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

func (cfg planCommandConfig) actionMode() bool {
	return cfg.StatusMode || cfg.CloseTaskID != "" || cfg.MergeTaskID != "" || cfg.CleanupMode
}

func validatePlanActionFlags(cfg planCommandConfig, limitRaw, sleepRaw string, hasAgentFlags bool, lg *log.Logger) exitcode.Code {
	if cfg.Format != "json" && cfg.Format != "table" {
		lg.Err("--format must be one of json,table, got: %s", cfg.Format)
		return exitcode.Env
	}
	if cfg.formatExplicit && !cfg.StatusMode {
		lg.Err("--format can only be used with --status")
		return exitcode.Invocation
	}

	lifecycleFlags := 0
	if cfg.CloseTaskID != "" {
		lifecycleFlags++
	}
	if cfg.MergeTaskID != "" {
		lifecycleFlags++
	}
	if cfg.CleanupMode {
		lifecycleFlags++
	}
	if cfg.StatusMode {
		switch {
		case lifecycleFlags > 0:
			return planStatusConflict(lg, planLifecycleFlagName(cfg))
		case hasAgentFlags:
			return planStatusConflict(lg, "--agent")
		case cfg.BaseBranch != "":
			return planStatusConflict(lg, "--base-branch")
		case limitRaw != "":
			return planStatusConflict(lg, "--limit")
		case cfg.OnlyArg != "":
			return planStatusConflict(lg, "--only")
		case cfg.SkipArg != "":
			return planStatusConflict(lg, "--skip")
		case cfg.Session != "":
			return planStatusConflict(lg, "--session")
		case cfg.DryRun:
			return planStatusConflict(lg, "--dry-run")
		case cfg.UnblockedOnly:
			return planStatusConflict(lg, "--unblocked-only")
		case cfg.Team:
			return planStatusConflict(lg, "--team")
		case cfg.NoRefresh:
			return planStatusConflict(lg, "--no-refresh")
		case cfg.AutoPullRequest != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--auto-pr", "--no-auto-pr", cfg.AutoPullRequest))
		case cfg.PRReviewGate != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--pr-review-gate", "--no-pr-review-gate", cfg.PRReviewGate))
		case cfg.BriefingCodeReview != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--briefing-code-review", "--no-briefing-code-review", cfg.BriefingCodeReview))
		case cfg.AgentTeamsHint != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--agent-teams-hint", "--no-agent-teams-hint", cfg.AgentTeamsHint))
		case cfg.PRVisualization != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--pr-visualization", "--no-pr-visualization", cfg.PRVisualization))
		case cfg.DashboardKeybind != nil:
			return planStatusConflict(lg, planBoolSettingFlag("--dashboard-keybind", "--no-dashboard-keybind", cfg.DashboardKeybind))
		case sleepRaw != "":
			return planStatusConflict(lg, "--sleep")
		}
	}

	if lifecycleFlags > 1 {
		lg.Err("--close, --merge, and --cleanup are mutually exclusive")
		return exitcode.Invocation
	}
	if lifecycleFlags > 0 {
		switch {
		case hasAgentFlags:
			return planLifecycleConflict(lg, "--agent")
		case cfg.BaseBranch != "":
			return planLifecycleConflict(lg, "--base-branch")
		case cfg.BranchPrefix != "":
			return planLifecycleConflict(lg, "--branch-prefix")
		case limitRaw != "":
			return planLifecycleConflict(lg, "--limit")
		case cfg.OnlyArg != "":
			return planLifecycleConflict(lg, "--only")
		case cfg.SkipArg != "":
			return planLifecycleConflict(lg, "--skip")
		case cfg.Session != "":
			return planLifecycleConflict(lg, "--session")
		case cfg.DryRun:
			return planLifecycleConflict(lg, "--dry-run")
		case cfg.UnblockedOnly:
			return planLifecycleConflict(lg, "--unblocked-only")
		case cfg.Team:
			return planLifecycleConflict(lg, "--team")
		case cfg.NoRefresh:
			return planLifecycleConflict(lg, "--no-refresh")
		case cfg.AutoPullRequest != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--auto-pr", "--no-auto-pr", cfg.AutoPullRequest))
		case cfg.PRReviewGate != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--pr-review-gate", "--no-pr-review-gate", cfg.PRReviewGate))
		case cfg.BriefingCodeReview != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--briefing-code-review", "--no-briefing-code-review", cfg.BriefingCodeReview))
		case cfg.AgentTeamsHint != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--agent-teams-hint", "--no-agent-teams-hint", cfg.AgentTeamsHint))
		case cfg.PRVisualization != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--pr-visualization", "--no-pr-visualization", cfg.PRVisualization))
		case cfg.DashboardKeybind != nil:
			return planLifecycleConflict(lg, planBoolSettingFlag("--dashboard-keybind", "--no-dashboard-keybind", cfg.DashboardKeybind))
		case sleepRaw != "":
			return planLifecycleConflict(lg, "--sleep")
		}
	}
	return exitcode.OK
}

func planStatusConflict(lg *log.Logger, flag string) exitcode.Code {
	lg.Err("--status cannot be combined with %s", flag)
	return exitcode.Invocation
}

func planLifecycleConflict(lg *log.Logger, flag string) exitcode.Code {
	lg.Err("--close/--merge/--cleanup cannot be combined with %s", flag)
	return exitcode.Invocation
}

func planLifecycleFlagName(cfg planCommandConfig) string {
	switch {
	case cfg.CloseTaskID != "":
		return "--close"
	case cfg.MergeTaskID != "":
		return "--merge"
	case cfg.CleanupMode:
		return "--cleanup"
	default:
		return "--close/--merge/--cleanup"
	}
}

func planBoolSettingFlag(onFlag, offFlag string, v *bool) string {
	if v != nil && *v {
		return onFlag
	}
	return offFlag
}

func validatePlanTaskID(flag, value string) error {
	if !rePlanTaskID.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase kebab-case task id, got: %s", flag, value)
	}
	return nil
}

func (cfg planCommandConfig) cliConfig() *cliflags.Config {
	return &cliflags.Config{
		ParentRef:          planSubcommand,
		Agent:              cfg.Agent,
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

func parsePlanAgentArg(cfg *planCommandConfig, raw string) error {
	if !strings.Contains(raw, "=") {
		cfg.Agent = raw
		return nil
	}
	eq := strings.IndexByte(raw, '=')
	target := raw[:eq]
	name := raw[eq+1:]

	if !rePlanTaskID.MatchString(target) {
		return fmt.Errorf("--agent: <task-id> must be lowercase kebab-case, got: %s", target)
	}
	if name == "" {
		return fmt.Errorf("--agent %s: agent name must not be empty", target)
	}
	cfg.AgentOverrides = cliflags.UpsertAgentOverride(cfg.AgentOverrides, target, name)
	return nil
}

func checkPlanDeps() []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("git", "git")
	check("tmux", "tmux (brew install tmux)")
	return missing
}

func checkPlanActionDeps(cfg planCommandConfig) []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	check("git", "git")
	if cfg.StatusMode || cfg.CleanupMode {
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
	return worktree.ResolveDefaultBranchAllowMissingOrigin(projectRoot), nil
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

type planStatusReport struct {
	Plan    planspec.Plan    `json:"plan"`
	Tasks   []planStatusTask `json:"tasks"`
	Summary statusSummary    `json:"summary"`
}

type planStatusTask struct {
	ID          string          `json:"id"`
	Branch      string          `json:"branch"`
	PRs         []ghissue.PRRef `json:"prs"`
	HasMergedPR bool            `json:"has_merged_pr"`
	Blocked     bool            `json:"blocked"`
}

func cmdPlanLifecycle(cfg planCommandConfig, lg *log.Logger) exitcode.Code {
	mode := planActionModeFlag(cfg)
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return code
	}
	cfg.SpecPath = resolvePlanSpecPath(rt.projectRoot, cfg.SpecArg)
	spec, code := loadPlanActionSpec(cfg, lg)
	if code != exitcode.OK {
		return code
	}
	parentRef := planParentRef(spec.Plan.Slug)

	if cfg.StatusMode {
		return cmdPlanStatus(cfg, spec, rt.projectRoot, rt.statePath, lg)
	}
	lifecycleOpts := lifecycle.Options{
		ProjectRoot: rt.projectRoot,
		StatePath:   rt.statePath,
		Hooks:       hooks.LoadUserConfig(lg),
	}

	switch {
	case cfg.CloseTaskID != "":
		return lifecycle.CloseTask(lifecycleOpts, parentRef, cfg.CloseTaskID, lg)
	case cfg.MergeTaskID != "":
		return lifecycle.MergeTask(lifecycleOpts, parentRef, cfg.MergeTaskID, lg)
	case cfg.CleanupMode:
		return lifecycle.CleanupPlan(lifecycleOpts, parentRef, lg)
	default:
		return exitcode.Invocation
	}
}

func planActionModeFlag(cfg planCommandConfig) string {
	switch {
	case cfg.StatusMode:
		return "--status"
	case cfg.CloseTaskID != "":
		return "--close"
	case cfg.MergeTaskID != "":
		return "--merge"
	case cfg.CleanupMode:
		return "--cleanup"
	default:
		return "plan"
	}
}

func loadPlanActionSpec(cfg planCommandConfig, lg *log.Logger) (planspec.Spec, exitcode.Code) {
	spec, err := planspec.LoadWithoutResolvedNameChecks(cfg.SpecPath)
	if err != nil {
		lg.Err("%s: %v", planActionModeFlag(cfg), err)
		return planspec.Spec{}, planActionInputCode(cfg)
	}
	if cfg.StatusMode {
		return validatePlanActionSpecNames(cfg, spec, lg)
	}
	return spec, exitcode.OK
}

func validatePlanActionSpecNames(cfg planCommandConfig, spec planspec.Spec, lg *log.Logger) (planspec.Spec, exitcode.Code) {
	if err := validatePlanExecutionNames(spec, cfg); err != nil {
		lg.Err("%s: validate plan execution names %s: %v", planActionModeFlag(cfg), cfg.SpecPath, err)
		return planspec.Spec{}, planActionInputCode(cfg)
	}
	return spec, exitcode.OK
}

func planActionInputCode(cfg planCommandConfig) exitcode.Code {
	if cfg.StatusMode {
		return exitcode.Invocation
	}
	return exitcode.Env
}

func cmdPlanStatus(cfg planCommandConfig, spec planspec.Spec, projectRoot, statePath string, lg *log.Logger) exitcode.Code {
	if projectRoot == "" || !dirExists(projectRoot) {
		lg.Err("--status: project_root is not a directory: %s (state=%s)", emptyLabel(projectRoot), statePath)
		return exitcode.Invocation
	}
	store, err := state.Load(statePath)
	if err != nil {
		lg.Err("--status: fanout state at %s is not valid JSON or has an invalid schema: %v", statePath, err)
		return exitcode.Invocation
	}
	report, code := buildPlanStatusReport(cfg, spec, projectRoot, store, lg)
	if code != exitcode.OK {
		return code
	}
	if cfg.Format == "table" {
		return writePlanStatusTable(report, projectRoot, lg)
	}
	return writePlanStatusReport(report, lg)
}

func buildPlanStatusReport(cfg planCommandConfig, spec planspec.Spec, projectRoot string, store state.Store, lg *log.Logger) (planStatusReport, exitcode.Code) {
	parentRef := planParentRef(spec.Plan.Slug)
	gh := ghissue.Runner{Cwd: projectRoot}
	tasks := make([]planStatusTask, 0, len(spec.Tasks))
	mergedByID := map[string]bool{}
	for _, task := range spec.Tasks {
		branch := planStatusBranch(cfg, spec, store, parentRef, task)
		prs, err := gh.PRsForBranch(branch)
		if err != nil {
			lg.Err("--status: gh pr list --head %s failed for task %s: %v", branch, task.ID, err)
			return planStatusReport{}, exitcode.GitHub
		}
		row := planStatusTask{
			ID:          task.ID,
			Branch:      branch,
			PRs:         prs,
			HasMergedPR: planHasMergedPR(prs),
		}
		mergedByID[task.ID] = row.HasMergedPR
		tasks = append(tasks, row)
	}
	for i, task := range spec.Tasks {
		tasks[i].Blocked = planTaskStatusBlocked(task, mergedByID)
	}
	return planStatusReport{
		Plan:    spec.Plan,
		Tasks:   tasks,
		Summary: newPlanStatusSummary(tasks),
	}, exitcode.OK
}

func planStatusBranch(cfg planCommandConfig, spec planspec.Spec, store state.Store, parent string, task planspec.Task) string {
	if branch := recordedTaskBranch(store, parent, task.ID); branch != "" {
		return branch
	}
	return planTaskBranch(cfg, spec, task)
}

func recordedTaskBranch(store state.Store, parent, taskID string) string {
	for _, pane := range store.PanesForParent(parent) {
		if pane.TaskID == taskID && strings.TrimSpace(pane.BranchName) != "" {
			return strings.TrimSpace(pane.BranchName)
		}
	}
	return ""
}

func planTaskBranch(cfg planCommandConfig, spec planspec.Spec, task planspec.Task) string {
	if task.Branch != "" {
		return task.Branch
	}
	return naming.BranchName("", cfg.BranchPrefix, planTaskSlug(spec.Plan.Slug, task))
}

func planTaskStatusBlocked(task planspec.Task, mergedByID map[string]bool) bool {
	if mergedByID[task.ID] {
		return false
	}
	for _, depID := range task.BlockedBy {
		if !mergedByID[depID] {
			return true
		}
	}
	return false
}

func newPlanStatusSummary(tasks []planStatusTask) statusSummary {
	merged := 0
	blocked := 0
	for _, task := range tasks {
		if task.HasMergedPR {
			merged++
		}
		if task.Blocked {
			blocked++
		}
	}
	return statusSummary{
		Total:     len(tasks),
		Merged:    merged,
		Pending:   len(tasks) - merged,
		Blocked:   blocked,
		AllMerged: len(tasks) > 0 && merged == len(tasks),
	}
}

func writePlanStatusReport(report planStatusReport, lg *log.Logger) exitcode.Code {
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		lg.Err("--status: failed to encode plan report: %v", err)
		return exitcode.GitHub
	}
	fmt.Fprintln(lg.Stdout(), string(out))
	return exitcode.OK
}

func writePlanStatusTable(report planStatusReport, projectRoot string, lg *log.Logger) exitcode.Code {
	rows, maxLines, addWidth, delWidth, code := planStatusTableRows(report, projectRoot, lg)
	if code != exitcode.OK {
		return code
	}

	out := lg.Stdout()
	fmt.Fprintf(out, "fanout plan status %s: total=%d merged=%d pending=%d blocked=%d all_merged=%t\n",
		report.Plan.Slug, report.Summary.Total, report.Summary.Merged, report.Summary.Pending, report.Summary.Blocked, report.Summary.AllMerged)
	if len(rows) == 0 {
		fmt.Fprintln(out, "(no plan tasks)")
		return exitcode.OK
	}

	diffWidth := statusDiffWidth(addWidth, delWidth)
	widths := []int{
		len("TASK"),
		len("STATE"),
		len("PR"),
		len("PR_STATE"),
		len("CI"),
		len("TYPE"),
		len("FILES"),
		diffWidth,
		len("LINK"),
	}
	for _, row := range rows {
		widths[0] = max(widths[0], len(row.Issue))
		widths[1] = max(widths[1], len(row.IssueState))
		widths[2] = max(widths[2], len(row.PR))
		widths[3] = max(widths[3], len(row.PRState))
		widths[4] = max(widths[4], len(row.CI))
		widths[5] = max(widths[5], len(row.Type))
		widths[6] = max(widths[6], len(row.Files))
	}

	headers := []string{"TASK", "STATE", "PR", "PR_STATE", "CI", "TYPE", "FILES", "DIFF", "LINK"}
	fmt.Fprintln(out, statusTableLine(headers, widths))
	separators := []string{
		strings.Repeat("-", widths[0]),
		strings.Repeat("-", widths[1]),
		strings.Repeat("-", widths[2]),
		strings.Repeat("-", widths[3]),
		strings.Repeat("-", widths[4]),
		strings.Repeat("-", widths[5]),
		strings.Repeat("-", widths[6]),
		strings.Repeat("-", diffWidth),
		strings.Repeat("-", widths[8]),
	}
	fmt.Fprintln(out, statusTableLine(separators, widths))

	colors := lg.Colors()
	for _, row := range rows {
		cols := []string{
			row.Issue,
			row.IssueState,
			row.PR,
			row.PRState,
			row.CI,
			row.Type,
			row.Files,
			renderStatusDiff(row, maxLines, addWidth, delWidth, diffWidth, colors),
			row.Link,
		}
		fmt.Fprintln(out, statusTableLine(cols, widths))
	}
	return exitcode.OK
}

func planStatusTableRows(report planStatusReport, projectRoot string, lg *log.Logger) ([]statusTableRow, int, int, int, exitcode.Code) {
	if len(report.Tasks) == 0 {
		return nil, 0, len("+0"), len("-0"), exitcode.OK
	}

	gh := ghissue.Runner{Cwd: projectRoot}
	nwo, err := gh.RepoNameWithOwner()
	if err != nil {
		lg.Err("--status: failed to resolve repo (gh repo view) in %s", projectRoot)
		return nil, 0, 0, 0, exitcode.GitHub
	}

	rows := make([]statusTableRow, 0, len(report.Tasks))
	maxLines := 0
	addWidth := len("+0")
	delWidth := len("-0")
	for _, task := range report.Tasks {
		if len(task.PRs) == 0 {
			rows = append(rows, statusTableRow{
				Issue:      task.ID,
				IssueState: planStatusState(task),
				PR:         "-",
				PRState:    "-",
				CI:         "-",
				Type:       "-",
				Files:      "-",
				Link:       "-",
			})
			continue
		}
		for _, pr := range task.PRs {
			stat, err := gh.PRDiffStat(pr.Number)
			if err != nil {
				lg.Err("--status: gh pr view #%d failed: %v", pr.Number, err)
				return nil, 0, 0, 0, exitcode.GitHub
			}
			addWidth = max(addWidth, len(fmt.Sprintf("+%d", stat.Additions)))
			delWidth = max(delWidth, len(fmt.Sprintf("-%d", stat.Deletions)))
			maxLines = max(maxLines, stat.Additions)
			maxLines = max(maxLines, stat.Deletions)
			rows = append(rows, statusTableRow{
				Issue:      task.ID,
				IssueState: planStatusState(task),
				PR:         "#" + strconv.Itoa(pr.Number),
				PRState:    dashIfEmpty(pr.DisplayState()),
				CI:         dashIfEmpty(pr.CIStatus),
				Type:       conventionalType(stat.Title),
				Files:      strconv.Itoa(stat.ChangedFiles),
				Link:       fmt.Sprintf("https://github.com/%s/pull/%d", nwo, pr.Number),
				Additions:  stat.Additions,
				Deletions:  stat.Deletions,
				HasDiff:    true,
			})
		}
	}
	return rows, maxLines, addWidth, delWidth, exitcode.OK
}

func planStatusState(task planStatusTask) string {
	if task.HasMergedPR {
		return "merged"
	}
	if task.Blocked {
		return "blocked"
	}
	return "pending"
}

func planHasMergedPR(prs []ghissue.PRRef) bool {
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "MERGED") || pr.MergedAt != nil {
			return true
		}
	}
	return false
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

func executeTaskPlan(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, spec planspec.Spec, targets []planspec.Task, resolvedSettings settings.Settings, hookConfig hooks.Config, recorder paneStateRecorder, c log.Palette, commandName string, teamCtx *briefing.TeamContext) taskExecutionResult {
	var result taskExecutionResult
	for i, task := range targets {
		req := newTaskPaneRequest(cfg, info.ProjectRoot, spec, task, resolvedSettings, hookConfig, teamCtx)
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
	fmt.Fprintf(lg.Stdout(), "  %s plan %s --only %s%s%s%s%s%s%s%s\n",
		shellQuote(commandName),
		shellQuote(cfg.SpecArg),
		shellQuote(ids),
		boolFlag(" --unblocked-only", cfg.UnblockedOnly),
		boolFlag(" --team", cfg.Team),
		settingsFlags(cliCfg),
		worktreeFlags(cliCfg),
		agentFlagsForTasks(cfg.Agent, cfg.AgentOverrides, plan.LimitDeferred),
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
  --agent <name|task-id=name> Agent to launch (or FANOUT_AGENT). Repeat for
                              per-task overrides.
  --dry-run                   Print worktree/tmux/agent actions without launching
  --status                    Print task status and exit
  --format <json|table>       Output format for --status (default: json)
  --close <task-id>           Remove a recorded task worktree, pane, and state row
  --merge <task-id>           Fast-forward merge the recorded task branch
  --cleanup                   Close task panes whose branch has a MERGED PR
  --limit <N>                 Create at most N panes
  --only <task-id[,id...]>    Restrict to task IDs
  --skip <task-id[,id...]>    Exclude task IDs
  --unblocked-only            Defer tasks whose blocked_by dependencies are incomplete
  --team                      Seed sibling-task peer messaging (fanout msg, addressed by task id)
  --base-branch <branch>      Override spec plan.base_branch
  --branch-prefix <prefix>    Prefix generated branch names
  --no-refresh                Do not fetch/fast-forward the base branch
  --session <tmux-session>    Target a tmux session instead of the invoking pane
  --sleep <seconds>           Delay between pane launches
  --debug                     Print debug diagnostics
`
