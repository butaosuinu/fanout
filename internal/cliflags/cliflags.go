// Package cliflags is a hand-rolled clone of the bash arg parser at
// fanout:308-383. The standard `flag` package can't reproduce the exact error
// wording or the `--name NUM=slug|disp` repeating shape, and Tier 1 tests
// pin every error message verbatim.
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
	DefaultSleepBetween = 4.0
	DefaultPopupTimeout = 20
)

// Config holds the parsed CLI invocation. Validation has already run when a
// non-nil Config is returned.
type Config struct {
	Parent          int
	Agent           string
	Limit           int // 0 = unset
	OnlyArg         string
	SkipArg         string
	IncludeArg      string
	Only            []int
	Skip            []int
	Include         []int
	Names           []NameOverride
	Session         string
	SleepBetween    float64
	PopupTimeoutSec int
	DryRun          bool
	Debug           bool
	UnblockedOnly   bool
}

// NameOverride represents a parsed `--name NUM=slug-hint[|display-name]` pair.
type NameOverride struct {
	Num         int
	SlugHint    string
	DisplayName string
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

// ParseResult communicates parse outcome to main without duplicating the exit
// machinery. When Code is non-OK, output has already been written.
type ParseResult struct {
	Config *Config
	Code   exitcode.Code
}

// Parse processes args (typically os.Args[1:]) and writes any usage / error
// output to stdout / stderr via lg. The returned exitcode signals success
// (OK) or what to exit with (Env=1 for validation, Invocation=2 for shape
// errors). stdout is the writer Usage() goes to on success (-h/--help);
// validation errors and "no positional" go through lg.Err / Usage(stderr).
func Parse(args []string, lg *log.Logger, stdout io.Writer) ParseResult {
	cfg := &Config{
		SleepBetween:    DefaultSleepBetween,
		PopupTimeoutSec: DefaultPopupTimeout,
	}

	state := parseState{}
	valueOptions := map[string]valueOption{
		"--agent": func(cfg *Config, _ *parseState, v string) error {
			cfg.Agent = v
			return nil
		},
		"--limit": func(_ *Config, state *parseState, v string) error {
			state.limit = v
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
		"--name": func(cfg *Config, _ *parseState, v string) error {
			return parseNameArg(cfg, v)
		},
		"--session": func(cfg *Config, _ *parseState, v string) error {
			cfg.Session = v
			return nil
		},
		"--sleep": func(_ *Config, state *parseState, v string) error {
			state.sleepRaw = v
			return nil
		},
		"--popup-timeout": func(_ *Config, state *parseState, v string) error {
			state.popupRaw = v
			return nil
		},
	}
	boolOptions := map[string]boolOption{
		"--dry-run":        func(cfg *Config) { cfg.DryRun = true },
		"--debug":          func(cfg *Config) { cfg.Debug = true },
		"--unblocked-only": func(cfg *Config) { cfg.UnblockedOnly = true },
	}

	requireValue := func(flag string, i int) (string, bool) {
		if i+1 >= len(args) {
			lg.Err("%s requires an argument", flag)
			return "", false
		}
		return args[i+1], true
	}

	i := 0
	for i < len(args) {
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
	parent   string
	limit    string
	sleepRaw string
	popupRaw string
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

	if !reAllDigits.MatchString(state.parent) {
		lg.Err("parent issue must be an integer, got: %s", state.parent)
		return ParseResult{Code: exitcode.Env}
	}
	cfg.Parent, _ = strconv.Atoi(state.parent)

	if state.limit != "" {
		if !rePositiveInt.MatchString(state.limit) {
			lg.Err("--limit must be a positive integer, got: %s", state.limit)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Limit, _ = strconv.Atoi(state.limit)
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

var (
	reAllDigits   = regexp.MustCompile(`^[0-9]+$`)
	rePositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)
	reNumber      = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	reKebab       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

func parseNumCSV(flag, csv string) ([]int, error) {
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
		return fmt.Errorf("--name requires <NUM>=<slug-hint>[|<display-name>], got: '%s'", raw)
	}
	eq := strings.IndexByte(raw, '=')
	num := raw[:eq]
	val := raw[eq+1:]

	if !reAllDigits.MatchString(num) {
		return fmt.Errorf("--name: <NUM> must be a positive integer, got: '%s'", num)
	}
	n, _ := strconv.Atoi(num)

	var slug, disp string
	if pipe := strings.IndexByte(val, '|'); pipe >= 0 {
		slug = val[:pipe]
		disp = val[pipe+1:]
	} else {
		slug = val
	}

	if slug == "" && disp == "" {
		return fmt.Errorf("--name #%d: both slug-hint and display-name are empty", n)
	}
	if slug != "" && !reKebab.MatchString(slug) {
		return fmt.Errorf("--name #%d: slug-hint must be lowercase kebab-case (alnum+hyphens, starting with alnum), got: '%s'", n, slug)
	}

	for i := range cfg.Names {
		if cfg.Names[i].Num == n {
			if slug != "" {
				cfg.Names[i].SlugHint = slug
			}
			if disp != "" {
				cfg.Names[i].DisplayName = disp
			}
			return nil
		}
	}
	cfg.Names = append(cfg.Names, NameOverride{Num: n, SlugHint: slug, DisplayName: disp})
	return nil
}
