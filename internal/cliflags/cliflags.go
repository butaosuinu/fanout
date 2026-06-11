// Package cliflags is a hand-rolled clone of the Bash arg parser. The standard
// flag package cannot reproduce the exact error wording, exit-code split, or
// repeatable --name shape that the black-box tests pin.
package cliflags

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
)

const (
	DefaultSleepBetween  = 4.0
	DefaultPopupTimeout  = 20
	DefaultProjectStatus = "Todo"
	DefaultFormat        = "json"

	ModeIssue   = "issue"
	ModeProject = "project"
)

// Config holds the parsed CLI invocation. Validation has already run when a
// non-nil Config is returned.
type Config struct {
	Parent             int
	ParentRef          string
	ParentMode         string
	ProjectOwnerType   string
	ProjectOwner       string
	ProjectNumber      int
	ProjectStatus      string
	Agent              string
	BaseBranch         string
	BranchPrefix       string
	Limit              int // 0 = unset
	OnlyArg            string
	SkipArg            string
	IncludeArg         string
	Only               []int
	Skip               []int
	Include            []int
	Names              []NameOverride
	Session            string
	SleepBetween       float64
	PopupTimeoutSec    int
	NoRefresh          bool
	DryRun             bool
	Debug              bool
	UnblockedOnly      bool
	StatusMode         bool
	PostDashboard      bool
	CloseNum           int
	MergeNum           int
	CleanupMode        bool
	AutoPullRequest    *bool
	PRReviewGate       *bool
	BriefingCodeReview *bool
	AgentTeamsHint     *bool
	CodexPlanMode      *bool
	PRVisualization    *bool
	DashboardKeybind   *bool
	Format             string
}

// NameOverride represents a parsed `--name NUM=slug-hint|display-name|branch`
// entry. Empty segments are allowed as long as at least one segment is present.
type NameOverride struct {
	Num         int
	SlugHint    string
	DisplayName string
	BranchName  string
}

// FindName returns a pointer to the override for issue num, or nil.
func (c *Config) FindName(num int) *NameOverride {
	for i := range c.Names {
		if c.Names[i].Num == num {
			return &c.Names[i]
		}
	}
	return nil
}

func (c *Config) HasAnyDisplayName() bool {
	for _, n := range c.Names {
		if n.DisplayName != "" {
			return true
		}
	}
	return false
}

func (c *Config) CodexPlanModeEnabled() bool {
	return c.CodexPlanMode != nil && *c.CodexPlanMode
}

// ParseResult communicates parse outcome to main without duplicating the exit
// machinery. When Code is non-OK, output has already been written.
type ParseResult struct {
	Config *Config
	Code   exitcode.Code
}

// Parse processes args (typically os.Args[1:]) and writes any usage / error
// output to stdout / stderr via lg.
func Parse(args []string, lg *log.Logger, stdout io.Writer) ParseResult {
	cfg := &Config{
		SleepBetween:    DefaultSleepBetween,
		PopupTimeoutSec: DefaultPopupTimeout,
		ProjectStatus:   DefaultProjectStatus,
		Format:          DefaultFormat,
	}

	state := parseState{}
	valueOptions := map[string]valueOption{
		"--agent": func(cfg *Config, _ *parseState, v string) error {
			cfg.Agent = v
			return nil
		},
		"--base-branch": func(cfg *Config, _ *parseState, v string) error {
			cfg.BaseBranch = v
			return nil
		},
		"--branch-prefix": func(cfg *Config, _ *parseState, v string) error {
			cfg.BranchPrefix = v
			return nil
		},
		"--limit": func(_ *Config, state *parseState, v string) error {
			state.limit = v
			return nil
		},
		"--close": func(_ *Config, state *parseState, v string) error {
			state.closeRaw = v
			return nil
		},
		"--merge": func(_ *Config, state *parseState, v string) error {
			state.mergeRaw = v
			return nil
		},
		"--only": func(cfg *Config, _ *parseState, v string) error {
			cfg.OnlyArg = v
			return nil
		},
		"--skip": func(cfg *Config, _ *parseState, v string) error {
			cfg.SkipArg = v
			return nil
		},
		"--include": func(cfg *Config, _ *parseState, v string) error {
			cfg.IncludeArg = v
			return nil
		},
		"--name": func(_ *Config, state *parseState, v string) error {
			state.rawNames = append(state.rawNames, v)
			return nil
		},
		"--session": func(cfg *Config, _ *parseState, v string) error {
			cfg.Session = v
			return nil
		},
		"--project-status": func(cfg *Config, _ *parseState, v string) error {
			cfg.ProjectStatus = v
			return nil
		},
		"--format": func(cfg *Config, state *parseState, v string) error {
			switch v {
			case "json", "table":
				cfg.Format = v
				state.formatExplicit = true
				return nil
			default:
				return fmt.Errorf("--format must be one of json,table, got: %s", v)
			}
		},
		"--sleep": func(_ *Config, state *parseState, v string) error {
			state.sleepRaw = v
			state.sleepExplicit = true
			return nil
		},
		"--popup-timeout": func(_ *Config, state *parseState, v string) error {
			state.popupRaw = v
			state.popupExplicit = true
			return nil
		},
	}
	boolOptions := map[string]boolOption{
		"--dry-run":                 func(cfg *Config) { cfg.DryRun = true },
		"--debug":                   func(cfg *Config) { cfg.Debug = true },
		"--no-refresh":              func(cfg *Config) { cfg.NoRefresh = true },
		"--unblocked-only":          func(cfg *Config) { cfg.UnblockedOnly = true },
		"--status":                  func(cfg *Config) { cfg.StatusMode = true },
		"--post-dashboard":          func(cfg *Config) { cfg.PostDashboard = true },
		"--cleanup":                 func(cfg *Config) { cfg.CleanupMode = true },
		"--auto-pr":                 func(cfg *Config) { cfg.AutoPullRequest = new(true) },
		"--no-auto-pr":              func(cfg *Config) { cfg.AutoPullRequest = new(false) },
		"--pr-review-gate":          func(cfg *Config) { cfg.PRReviewGate = new(true) },
		"--no-pr-review-gate":       func(cfg *Config) { cfg.PRReviewGate = new(false) },
		"--briefing-code-review":    func(cfg *Config) { cfg.BriefingCodeReview = new(true) },
		"--no-briefing-code-review": func(cfg *Config) { cfg.BriefingCodeReview = new(false) },
		"--agent-teams-hint":        func(cfg *Config) { cfg.AgentTeamsHint = new(true) },
		"--no-agent-teams-hint":     func(cfg *Config) { cfg.AgentTeamsHint = new(false) },
		"--codex-plan-mode":         func(cfg *Config) { cfg.CodexPlanMode = new(true) },
		"--no-codex-plan-mode":      func(cfg *Config) { cfg.CodexPlanMode = new(false) },
		"--pr-visualization":        func(cfg *Config) { cfg.PRVisualization = new(true) },
		"--no-pr-visualization":     func(cfg *Config) { cfg.PRVisualization = new(false) },
		"--dashboard-keybind":       func(cfg *Config) { cfg.DashboardKeybind = new(true) },
		"--no-dashboard-keybind":    func(cfg *Config) { cfg.DashboardKeybind = new(false) },
	}

	requireValue := func(flag string, i int) (string, bool) {
		if i+1 >= len(args) {
			lg.Err("%s requires an argument", flag)
			return "", false
		}
		return args[i+1], true
	}

	for i := 0; i < len(args); {
		a := args[i]
		if a == "-h" || a == "--help" {
			Usage(stdout)
			return ParseResult{Code: exitcode.OK}
		}

		if a == "--" {
			i++
			for ; i < len(args); i++ {
				if !setParent(&state.parent, args[i], lg) {
					Usage(lg.Stderr())
					return ParseResult{Code: exitcode.Invocation}
				}
			}
			break
		}

		if handle, ok := valueOptions[a]; ok {
			v, ok := requireValue(a, i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			if err := handle(cfg, &state, v); err != nil {
				lg.Err("%s", err.Error())
				return ParseResult{Code: exitcode.Env}
			}
			i += 2
			continue
		}

		if handle, ok := boolOptions[a]; ok {
			handle(cfg)
			i++
			continue
		}

		switch {
		case strings.HasPrefix(a, "-"):
			lg.Err("unknown option: %s", a)
			Usage(lg.Stderr())
			return ParseResult{Code: exitcode.Invocation}
		default:
			if !setParent(&state.parent, a, lg) {
				Usage(lg.Stderr())
				return ParseResult{Code: exitcode.Invocation}
			}
			i++
		}
	}

	return validateParsed(cfg, state, lg)
}

type parseState struct {
	parent         string
	limit          string
	closeRaw       string
	mergeRaw       string
	sleepRaw       string
	popupRaw       string
	rawNames       []string
	sleepExplicit  bool
	popupExplicit  bool
	formatExplicit bool
}

type valueOption func(*Config, *parseState, string) error
type boolOption func(*Config)

func setParent(parent *string, arg string, lg *log.Logger) bool {
	if *parent == "" {
		*parent = arg
		return true
	}
	lg.Err("unexpected positional argument: %s", arg)
	return false
}

func validateParsed(cfg *Config, state parseState, lg *log.Logger) ParseResult {
	if state.parent == "" {
		Usage(lg.Stderr())
		return ParseResult{Code: exitcode.Invocation}
	}

	if reAllDigits.MatchString(state.parent) {
		cfg.ParentMode = ModeIssue
		cfg.Parent, _ = strconv.Atoi(state.parent)
		cfg.ParentRef = strconv.Itoa(cfg.Parent)
	} else if m := reProjectURL.FindStringSubmatch(state.parent); len(m) == 5 {
		cfg.ParentMode = ModeProject
		cfg.ProjectOwnerType = m[1]
		cfg.ProjectOwner = m[2]
		cfg.ProjectNumber, _ = strconv.Atoi(m[3])
		cfg.ParentRef = fmt.Sprintf("https://github.com/%s/%s/projects/%d", cfg.ProjectOwnerType, cfg.ProjectOwner, cfg.ProjectNumber)
	} else {
		if cfg.StatusMode {
			lg.Err("--status: parent must be an integer issue number or Projects v2 URL, got: %s", state.parent)
			return ParseResult{Code: exitcode.Invocation}
		}
		lg.Err("parent must be an integer issue number or Projects v2 URL, got: %s", state.parent)
		return ParseResult{Code: exitcode.Env}
	}

	if cfg.ProjectStatus == "" {
		lg.Err("--project-status must not be empty")
		return ParseResult{Code: exitcode.Env}
	}
	if state.formatExplicit && !cfg.StatusMode {
		lg.Err("--format can only be used with --status")
		return ParseResult{Code: exitcode.Invocation}
	}
	if cfg.PostDashboard && !cfg.StatusMode {
		lg.Err("--post-dashboard can only be used with --status")
		return ParseResult{Code: exitcode.Invocation}
	}

	if cfg.StatusMode {
		if cfg.ParentMode == ModeProject {
			lg.Err("--status cannot be combined with Projects v2 URLs as parent")
			return ParseResult{Code: exitcode.Invocation}
		}
		switch {
		case state.closeRaw != "":
			return statusConflict(lg, "--close")
		case state.mergeRaw != "":
			return statusConflict(lg, "--merge")
		case cfg.CleanupMode:
			return statusConflict(lg, "--cleanup")
		case cfg.Agent != "":
			return statusConflict(lg, "--agent")
		case cfg.BaseBranch != "":
			return statusConflict(lg, "--base-branch")
		case cfg.BranchPrefix != "":
			return statusConflict(lg, "--branch-prefix")
		case state.limit != "":
			return statusConflict(lg, "--limit")
		case cfg.OnlyArg != "":
			return statusConflict(lg, "--only")
		case cfg.SkipArg != "":
			return statusConflict(lg, "--skip")
		case cfg.IncludeArg != "":
			return statusConflict(lg, "--include")
		case len(state.rawNames) > 0:
			return statusConflict(lg, "--name")
		case cfg.Session != "":
			return statusConflict(lg, "--session")
		case cfg.DryRun:
			return statusConflict(lg, "--dry-run")
		case cfg.UnblockedOnly:
			return statusConflict(lg, "--unblocked-only")
		case cfg.NoRefresh:
			return statusConflict(lg, "--no-refresh")
		case cfg.AutoPullRequest != nil:
			return statusConflict(lg, boolSettingFlag("--auto-pr", "--no-auto-pr", cfg.AutoPullRequest))
		case cfg.PRReviewGate != nil:
			return statusConflict(lg, boolSettingFlag("--pr-review-gate", "--no-pr-review-gate", cfg.PRReviewGate))
		case cfg.BriefingCodeReview != nil:
			return statusConflict(lg, boolSettingFlag("--briefing-code-review", "--no-briefing-code-review", cfg.BriefingCodeReview))
		case cfg.AgentTeamsHint != nil:
			return statusConflict(lg, boolSettingFlag("--agent-teams-hint", "--no-agent-teams-hint", cfg.AgentTeamsHint))
		case cfg.CodexPlanMode != nil:
			return statusConflict(lg, boolSettingFlag("--codex-plan-mode", "--no-codex-plan-mode", cfg.CodexPlanMode))
		case cfg.PRVisualization != nil:
			return statusConflict(lg, boolSettingFlag("--pr-visualization", "--no-pr-visualization", cfg.PRVisualization))
		case cfg.DashboardKeybind != nil:
			return statusConflict(lg, boolSettingFlag("--dashboard-keybind", "--no-dashboard-keybind", cfg.DashboardKeybind))
		case state.sleepExplicit:
			return statusConflict(lg, "--sleep")
		case state.popupExplicit:
			return statusConflict(lg, "--popup-timeout")
		}
	}

	lifecycleFlags := 0
	if state.closeRaw != "" {
		lifecycleFlags++
	}
	if state.mergeRaw != "" {
		lifecycleFlags++
	}
	if cfg.CleanupMode {
		lifecycleFlags++
	}
	if lifecycleFlags > 1 {
		lg.Err("--close, --merge, and --cleanup are mutually exclusive")
		return ParseResult{Code: exitcode.Invocation}
	}
	if lifecycleFlags > 0 {
		switch {
		case cfg.Agent != "":
			return lifecycleConflict(lg, "--agent")
		case cfg.BaseBranch != "":
			return lifecycleConflict(lg, "--base-branch")
		case cfg.BranchPrefix != "":
			return lifecycleConflict(lg, "--branch-prefix")
		case state.limit != "":
			return lifecycleConflict(lg, "--limit")
		case cfg.OnlyArg != "":
			return lifecycleConflict(lg, "--only")
		case cfg.SkipArg != "":
			return lifecycleConflict(lg, "--skip")
		case cfg.IncludeArg != "":
			return lifecycleConflict(lg, "--include")
		case len(state.rawNames) > 0:
			return lifecycleConflict(lg, "--name")
		case cfg.Session != "":
			return lifecycleConflict(lg, "--session")
		case cfg.DryRun:
			return lifecycleConflict(lg, "--dry-run")
		case cfg.UnblockedOnly:
			return lifecycleConflict(lg, "--unblocked-only")
		case cfg.NoRefresh:
			return lifecycleConflict(lg, "--no-refresh")
		case cfg.AutoPullRequest != nil:
			return lifecycleConflict(lg, boolSettingFlag("--auto-pr", "--no-auto-pr", cfg.AutoPullRequest))
		case cfg.PRReviewGate != nil:
			return lifecycleConflict(lg, boolSettingFlag("--pr-review-gate", "--no-pr-review-gate", cfg.PRReviewGate))
		case cfg.BriefingCodeReview != nil:
			return lifecycleConflict(lg, boolSettingFlag("--briefing-code-review", "--no-briefing-code-review", cfg.BriefingCodeReview))
		case cfg.AgentTeamsHint != nil:
			return lifecycleConflict(lg, boolSettingFlag("--agent-teams-hint", "--no-agent-teams-hint", cfg.AgentTeamsHint))
		case cfg.CodexPlanMode != nil:
			return lifecycleConflict(lg, boolSettingFlag("--codex-plan-mode", "--no-codex-plan-mode", cfg.CodexPlanMode))
		case cfg.PRVisualization != nil:
			return lifecycleConflict(lg, boolSettingFlag("--pr-visualization", "--no-pr-visualization", cfg.PRVisualization))
		case cfg.DashboardKeybind != nil:
			return lifecycleConflict(lg, boolSettingFlag("--dashboard-keybind", "--no-dashboard-keybind", cfg.DashboardKeybind))
		case state.sleepExplicit:
			return lifecycleConflict(lg, "--sleep")
		case state.popupExplicit:
			return lifecycleConflict(lg, "--popup-timeout")
		}
	}

	for _, raw := range state.rawNames {
		if err := parseNameArg(cfg, raw); err != nil {
			lg.Err("%s", err.Error())
			return ParseResult{Code: exitcode.Env}
		}
	}

	if state.limit != "" {
		if !rePositiveInt.MatchString(state.limit) {
			lg.Err("--limit must be a positive integer, got: %s", state.limit)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Limit, _ = strconv.Atoi(state.limit)
	}

	if state.closeRaw != "" {
		if !rePositiveInt.MatchString(state.closeRaw) {
			lg.Err("--close must be a positive integer, got: %s", state.closeRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.CloseNum, _ = strconv.Atoi(state.closeRaw)
	}
	if state.mergeRaw != "" {
		if !rePositiveInt.MatchString(state.mergeRaw) {
			lg.Err("--merge must be a positive integer, got: %s", state.mergeRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.MergeNum, _ = strconv.Atoi(state.mergeRaw)
	}

	if cfg.BaseBranch != "" && strings.ContainsAny(cfg.BaseBranch, " \t\r\n") {
		lg.Err("--base-branch must not contain whitespace, got: %s", cfg.BaseBranch)
		return ParseResult{Code: exitcode.Env}
	}
	if cfg.BranchPrefix != "" && strings.ContainsAny(cfg.BranchPrefix, " \t\r\n") {
		lg.Err("--branch-prefix must not contain whitespace, got: %s", cfg.BranchPrefix)
		return ParseResult{Code: exitcode.Env}
	}

	if state.sleepRaw != "" {
		if !reNumber.MatchString(state.sleepRaw) {
			lg.Err("--sleep must be a number, got: %s", state.sleepRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.SleepBetween, _ = strconv.ParseFloat(state.sleepRaw, 64)
	}

	if state.popupRaw != "" {
		if !rePositiveInt.MatchString(state.popupRaw) {
			lg.Err("--popup-timeout must be a positive integer (seconds), got: %s", state.popupRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.PopupTimeoutSec, _ = strconv.Atoi(state.popupRaw)
	}

	if cfg.OnlyArg != "" && cfg.SkipArg != "" {
		lg.Err("--only and --skip are mutually exclusive")
		return ParseResult{Code: exitcode.Env}
	}

	if cfg.OnlyArg != "" {
		nums, err := parseNumCSV("--only", cfg.OnlyArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Only = nums
	}
	if cfg.SkipArg != "" {
		nums, err := parseNumCSV("--skip", cfg.SkipArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Skip = nums
	}
	if cfg.IncludeArg != "" {
		nums, err := parseNumCSV("--include", cfg.IncludeArg)
		if err != nil {
			lg.Err("%s", err.Error())
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Include = nums
	}

	return ParseResult{Config: cfg, Code: exitcode.OK}
}

func statusConflict(lg *log.Logger, flag string) ParseResult {
	lg.Err("--status cannot be combined with %s", flag)
	return ParseResult{Code: exitcode.Invocation}
}

func lifecycleConflict(lg *log.Logger, flag string) ParseResult {
	lg.Err("--close/--merge/--cleanup cannot be combined with %s", flag)
	return ParseResult{Code: exitcode.Invocation}
}

func boolSettingFlag(onFlag, offFlag string, v *bool) string {
	if v != nil && *v {
		return onFlag
	}
	return offFlag
}

var (
	reAllDigits   = regexp.MustCompile(`^[0-9]+$`)
	rePositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)
	reNumber      = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	reKebab       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	reProjectURL  = regexp.MustCompile(`^https://github\.com/(users|orgs)/([^/]+)/projects/([0-9]+)([/?].*)?$`)
)

func parseNumCSV(flag, csv string) ([]int, error) {
	csv = strings.TrimSuffix(csv, ",")
	parts := strings.Split(csv, ",")
	out := make([]int, 0, len(parts))
	for _, tok := range parts {
		if !reAllDigits.MatchString(tok) {
			return nil, fmt.Errorf("%s: invalid entry '%s' (must be a positive integer)", flag, tok)
		}
		n, _ := strconv.Atoi(tok)
		out = append(out, n)
	}
	return out, nil
}

func parseNameArg(cfg *Config, raw string) error {
	if !strings.Contains(raw, "=") {
		return fmt.Errorf("--name requires <NUM>=<slug-hint>[|<display-name>[|<branch-name>]], got: '%s'", raw)
	}
	eq := strings.IndexByte(raw, '=')
	num := raw[:eq]
	val := raw[eq+1:]

	if !reAllDigits.MatchString(num) {
		return fmt.Errorf("--name: <NUM> must be a positive integer, got: '%s'", num)
	}
	n, _ := strconv.Atoi(num)

	segs := strings.Split(val, "|")
	if len(segs) > 3 {
		return fmt.Errorf("--name #%d: too many '|'-separated segments (max 3: slug|display|branch), got: '%s'", n, val)
	}
	slug := segs[0]
	disp := ""
	branch := ""
	if len(segs) > 1 {
		disp = segs[1]
	}
	if len(segs) > 2 {
		branch = segs[2]
	}

	if slug == "" && disp == "" && branch == "" {
		return fmt.Errorf("--name #%d: slug-hint, display-name, and branch-name are all empty", n)
	}
	if slug != "" && !reKebab.MatchString(slug) {
		return fmt.Errorf("--name #%d: slug-hint must be lowercase kebab-case (alnum+hyphens, starting with alnum), got: '%s'", n, slug)
	}
	if branch != "" && strings.ContainsAny(branch, " \t\r\n") {
		return fmt.Errorf("--name #%d: branch-name must not contain whitespace, got: '%s'", n, branch)
	}

	for i := range cfg.Names {
		if cfg.Names[i].Num == n {
			if slug != "" {
				cfg.Names[i].SlugHint = slug
			}
			if disp != "" {
				cfg.Names[i].DisplayName = disp
			}
			if branch != "" {
				cfg.Names[i].BranchName = branch
			}
			return nil
		}
	}
	cfg.Names = append(cfg.Names, NameOverride{Num: n, SlugHint: slug, DisplayName: disp, BranchName: branch})
	return nil
}
