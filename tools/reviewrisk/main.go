package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run parses flags, loads the base..working-tree diff, evaluates the risk, and
// writes the chosen format to stdout. It returns 0 on success, 1 only when
// --fail-at is set and the level reaches it, and 2 for any operational error
// (bad flag, unknown format/level, or a failed git call). os.Exit lives only in
// main so run stays testable.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reviewrisk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "diff base ref (default: origin/main, then main)")
	format := fs.String("format", "text", "output format: text|markdown|json")
	failAt := fs.String("fail-at", "", "exit 1 when the level reaches this (none|low|medium|high|critical)")
	if err := fs.Parse(args); err != nil {
		// -h/--help is not an error: flag already printed usage, exit 0.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	switch *format {
	case "text", "markdown", "json":
	default:
		fmt.Fprintf(stderr, "reviewrisk: invalid --format %q (want text|markdown|json)\n", *format)
		return 2
	}

	var failLevel Level
	haveFailAt := *failAt != ""
	if haveFailAt {
		lv, err := parseLevel(*failAt)
		if err != nil {
			fmt.Fprintf(stderr, "reviewrisk: %v\n", err)
			return 2
		}
		failLevel = lv
	}

	baseRef, err := resolveBase(*base)
	if err != nil {
		fmt.Fprintf(stderr, "reviewrisk: %v\n", err)
		return 2
	}
	d, err := loadDiff(baseRef)
	if err != nil {
		fmt.Fprintf(stderr, "reviewrisk: %v\n", err)
		return 2
	}

	report := evaluate(d)
	var out string
	switch *format {
	case "text":
		out = report.Text()
	case "markdown":
		out = report.Markdown()
	case "json":
		b, err := report.JSON()
		if err != nil {
			fmt.Fprintf(stderr, "reviewrisk: %v\n", err)
			return 2
		}
		out = string(b)
	}
	fmt.Fprintln(stdout, out)

	if haveFailAt && report.Level >= failLevel {
		return 1
	}
	return 0
}
