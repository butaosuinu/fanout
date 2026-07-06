package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/run"
	"github.com/butaosuinu/fanout/internal/app/statusreport"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

const planSubcommand = "plan"

var (
	rePlanPositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)
	rePlanNumber      = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	// rePlanTaskID aliases the shared task-id shape (team.TaskIDRE) so the
	// plan CLI, the msg CLI parser, and internal/app/peermsg agree on one
	// definition.
	rePlanTaskID = team.TaskIDRE
)

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
	if cfg.ActionMode() {
		if exitOnMissingDeps(missingDeps(depNeeds{git: true, gh: cfg.StatusMode || cfg.CleanupMode}), lg) {
			return exitcode.Env
		}
		return cmdPlanLifecycle(cfg, lg)
	}

	if exitOnMissingDeps(missingDeps(depNeeds{git: true, tmux: true}), lg) {
		return exitcode.Env
	}

	cliCfg := cfg.CLIConfig()
	rt, code := run.ResolveRuntime(cliCfg, lg)
	if code != exitcode.OK {
		return code
	}
	cfg.Agent = cliCfg.Agent

	_, code = run.PlanTasks(cfg, rt, lg, commandName, bindDashboardKey)
	return code
}

func parsePlanCommand(args []string, lg *log.Logger) (run.PlanCommandConfig, exitcode.Code) {
	cfg := run.PlanCommandConfig{SleepBetween: cliflags.DefaultSleepBetween, Format: cliflags.DefaultFormat}
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
			cfg.FormatExplicit = true
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
				return run.PlanCommandConfig{}, exitcode.Env
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
			return run.PlanCommandConfig{}, exitcode.Invocation
		}
		if cfg.SpecArg != "" {
			lg.Err("unexpected plan argument: %s", arg)
			fmt.Fprint(lg.Stderr(), planUsage)
			return run.PlanCommandConfig{}, exitcode.Invocation
		}
		cfg.SpecArg = arg
		i++
	}

	if cfg.SpecArg == "" {
		fmt.Fprint(lg.Stderr(), planUsage)
		return run.PlanCommandConfig{}, exitcode.Invocation
	}
	if code := validatePlanActionFlags(cfg, limitRaw, sleepRaw, len(rawAgents) > 0, lg); code != exitcode.OK {
		return run.PlanCommandConfig{}, code
	}
	for _, raw := range rawAgents {
		if err := parsePlanAgentArg(&cfg, raw); err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
	}
	if limitRaw != "" {
		n, err := parsePlanPositiveInt("--limit", limitRaw)
		if err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
		cfg.Limit = n
	}
	if sleepRaw != "" {
		if !rePlanNumber.MatchString(sleepRaw) {
			lg.Err("--sleep must be a number, got: %s", sleepRaw)
			return run.PlanCommandConfig{}, exitcode.Env
		}
		n, _ := strconv.ParseFloat(sleepRaw, 64)
		cfg.SleepBetween = n
	}
	if err := run.ValidatePlanBaseBranch("--base-branch", cfg.BaseBranch); err != nil {
		lg.Err("%v", err)
		return run.PlanCommandConfig{}, exitcode.Env
	}
	if cfg.BranchPrefix != "" && strings.ContainsAny(cfg.BranchPrefix, " \t\r\n") {
		lg.Err("--branch-prefix must not contain whitespace, got: %s", cfg.BranchPrefix)
		return run.PlanCommandConfig{}, exitcode.Env
	}
	if cfg.CloseTaskID != "" {
		if err := validatePlanTaskID("--close", cfg.CloseTaskID); err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
	}
	if cfg.MergeTaskID != "" {
		if err := validatePlanTaskID("--merge", cfg.MergeTaskID); err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
	}
	if cfg.OnlyArg != "" && cfg.SkipArg != "" {
		lg.Err("--only and --skip are mutually exclusive")
		return run.PlanCommandConfig{}, exitcode.Env
	}
	var err error
	if cfg.OnlyArg != "" {
		cfg.Only, err = parseTaskIDCSV("--only", cfg.OnlyArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
	}
	if cfg.SkipArg != "" {
		cfg.Skip, err = parseTaskIDCSV("--skip", cfg.SkipArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return run.PlanCommandConfig{}, exitcode.Env
		}
	}
	return cfg, exitcode.OK
}

func validatePlanActionFlags(cfg run.PlanCommandConfig, limitRaw, sleepRaw string, hasAgentFlags bool, lg *log.Logger) exitcode.Code {
	if cfg.Format != "json" && cfg.Format != "table" {
		lg.Err("--format must be one of json,table, got: %s", cfg.Format)
		return exitcode.Env
	}
	if cfg.FormatExplicit && !cfg.StatusMode {
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

func planLifecycleFlagName(cfg run.PlanCommandConfig) string {
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

func parsePlanAgentArg(cfg *run.PlanCommandConfig, raw string) error {
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

func cmdPlanLifecycle(cfg run.PlanCommandConfig, lg *log.Logger) exitcode.Code {
	mode := planActionModeFlag(cfg)
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return code
	}
	cfg.SpecPath = run.ResolvePlanSpecPath(rt.projectRoot, cfg.SpecArg)
	spec, code := loadPlanActionSpec(cfg, lg)
	if code != exitcode.OK {
		return code
	}
	parentRef := panelaunch.PlanParentRef(spec.Plan.Slug)

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

func planActionModeFlag(cfg run.PlanCommandConfig) string {
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

func loadPlanActionSpec(cfg run.PlanCommandConfig, lg *log.Logger) (planspec.Spec, exitcode.Code) {
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

func validatePlanActionSpecNames(cfg run.PlanCommandConfig, spec planspec.Spec, lg *log.Logger) (planspec.Spec, exitcode.Code) {
	if err := run.ValidatePlanExecutionNames(spec, cfg); err != nil {
		lg.Err("%s: validate plan execution names %s: %v", planActionModeFlag(cfg), cfg.SpecPath, err)
		return planspec.Spec{}, planActionInputCode(cfg)
	}
	return spec, exitcode.OK
}

func planActionInputCode(cfg run.PlanCommandConfig) exitcode.Code {
	if cfg.StatusMode {
		return exitcode.Invocation
	}
	return exitcode.Env
}

func cmdPlanStatus(cfg run.PlanCommandConfig, spec planspec.Spec, projectRoot, statePath string, lg *log.Logger) exitcode.Code {
	if projectRoot == "" || !dirExists(projectRoot) {
		lg.Err("--status: project_root is not a directory: %s (state=%s)", emptyLabel(projectRoot), statePath)
		return exitcode.Invocation
	}
	store, err := state.Load(statePath)
	if err != nil {
		lg.Err("--status: fanout state at %s is not valid JSON or has an invalid schema: %v", statePath, err)
		return exitcode.Invocation
	}
	parentRef := panelaunch.PlanParentRef(spec.Plan.Slug)
	report, code := statusreport.BuildPlanReport(spec, projectRoot, func(task planspec.Task) string {
		return planStatusBranch(cfg, spec, store, parentRef, task)
	}, lg)
	if code != exitcode.OK {
		return code
	}
	if cfg.Format == "table" {
		return statusreport.WritePlanTable(report, projectRoot, lg)
	}
	return statusreport.WritePlanReport(report, lg)
}

func planStatusBranch(cfg run.PlanCommandConfig, spec planspec.Spec, store state.Store, parent string, task planspec.Task) string {
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

func planTaskBranch(cfg run.PlanCommandConfig, spec planspec.Spec, task planspec.Task) string {
	if task.Branch != "" {
		return task.Branch
	}
	return naming.BranchName("", cfg.BranchPrefix, panelaunch.PlanTaskSlug(spec.Plan.Slug, task))
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
