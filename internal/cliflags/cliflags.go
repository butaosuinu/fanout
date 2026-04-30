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

	parent := ""
	limit := ""
	sleepRaw := ""
	popupRaw := ""

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
		switch {
		case a == "-h" || a == "--help":
			Usage(stdout)
			return ParseResult{Code: exitcode.OK}
		case a == "--agent":
			v, ok := requireValue("--agent", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			cfg.Agent = v
			i += 2
		case a == "--limit":
			v, ok := requireValue("--limit", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			limit = v
			i += 2
		case a == "--only":
			v, ok := requireValue("--only", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			cfg.OnlyArg = v
			i += 2
		case a == "--skip":
			v, ok := requireValue("--skip", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			cfg.SkipArg = v
			i += 2
		case a == "--include":
			v, ok := requireValue("--include", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			cfg.IncludeArg = v
			i += 2
		case a == "--name":
			v, ok := requireValue("--name", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			if err := parseNameArg(cfg, v); err != nil {
				lg.Err("%s", err.Error())
				return ParseResult{Code: exitcode.Env}
			}
			i += 2
		case a == "--session":
			v, ok := requireValue("--session", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			cfg.Session = v
			i += 2
		case a == "--sleep":
			v, ok := requireValue("--sleep", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			sleepRaw = v
			i += 2
		case a == "--popup-timeout":
			v, ok := requireValue("--popup-timeout", i)
			if !ok {
				return ParseResult{Code: exitcode.Env}
			}
			popupRaw = v
			i += 2
		case a == "--dry-run":
			cfg.DryRun = true
			i++
		case a == "--debug":
			cfg.Debug = true
			i++
		case a == "--unblocked-only":
			cfg.UnblockedOnly = true
			i++
		case a == "--":
			i++
			// remaining args treated as positionals
			for j := i; j < len(args); j++ {
				if parent == "" {
					parent = args[j]
				} else {
					lg.Err("unexpected positional argument: %s", args[j])
					Usage(lg.Stderr())
					return ParseResult{Code: exitcode.Invocation}
				}
			}
			i = len(args)
		case strings.HasPrefix(a, "-"):
			lg.Err("unknown option: %s", a)
			Usage(lg.Stderr())
			return ParseResult{Code: exitcode.Invocation}
		default:
			if parent == "" {
				parent = a
			} else {
				lg.Err("unexpected positional argument: %s", a)
				Usage(lg.Stderr())
				return ParseResult{Code: exitcode.Invocation}
			}
			i++
		}
	}

	if parent == "" {
		Usage(lg.Stderr())
		return ParseResult{Code: exitcode.Invocation}
	}

	if !reAllDigits.MatchString(parent) {
		lg.Err("parent issue must be an integer, got: %s", parent)
		return ParseResult{Code: exitcode.Env}
	}
	cfg.Parent, _ = strconv.Atoi(parent)

	if limit != "" {
		if !rePositiveInt.MatchString(limit) {
			lg.Err("--limit must be a positive integer, got: %s", limit)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.Limit, _ = strconv.Atoi(limit)
	}

	if sleepRaw != "" {
		if !reNumber.MatchString(sleepRaw) {
			lg.Err("--sleep must be a number, got: %s", sleepRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.SleepBetween, _ = strconv.ParseFloat(sleepRaw, 64)
	}

	if popupRaw != "" {
		if !rePositiveInt.MatchString(popupRaw) {
			lg.Err("--popup-timeout must be a positive integer (seconds), got: %s", popupRaw)
			return ParseResult{Code: exitcode.Env}
		}
		cfg.PopupTimeoutSec, _ = strconv.Atoi(popupRaw)
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
