package main

import (
	"fmt"
	"os/exec"

	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
)

// depNeeds selects which external dependencies a command path requires.
// Callers own the per-mode conditions; missingDeps owns the probes and the
// hint strings.
type depNeeds struct {
	git  bool
	gh   bool
	tmux bool
}

// missingDeps probes the selected dependencies and returns one hint line per
// missing one, in the fixed git → gh → tmux order the CLI has always printed.
func missingDeps(needs depNeeds) []string {
	var missing []string
	check := func(cmd, hint string) {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, hint)
		}
	}
	if needs.git {
		check("git", "git")
	}
	if needs.gh {
		check("gh", "gh (brew install gh)")
	}
	if needs.tmux {
		if err := paneruntime.NewTmux().CheckAvailable(); err != nil {
			missing = append(missing, err.Error())
		}
	}
	return missing
}

// exitOnMissingDeps prints the shared "missing dependencies:" block to stderr
// and reports whether the caller should exit. An empty list prints nothing.
func exitOnMissingDeps(missing []string, lg *log.Logger) bool {
	if len(missing) == 0 {
		return false
	}
	lg.Err("missing dependencies:")
	for _, d := range missing {
		fmt.Fprintf(lg.Stderr(), "  - %s\n", d)
	}
	return true
}
