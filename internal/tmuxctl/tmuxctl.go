// Package tmuxctl wraps the few `tmux` subcommands fanout invokes during
// pane creation. dmuxsession owns show-options / list-sessions; this package
// owns send-keys.
package tmuxctl

import (
	"fmt"
	"io"
	"os/exec"
)

// SendKeys fires a single named key at controlPane.
func SendKeys(controlPane, key string) error {
	return exec.Command("tmux", "send-keys", "-t", controlPane, key).Run()
}

// PrintSendKeys writes the dry-run-equivalent shell invocation to w. The exact
// format is locked in by the Tier 2 goldens (`    $ tmux send-keys -t %P key`).
func PrintSendKeys(w io.Writer, dim, reset, controlPane, key string) {
	fmt.Fprintf(w, "    %s$ tmux send-keys -t %s %s%s\n", dim, controlPane, key, reset)
}
