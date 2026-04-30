// Package tmuxctl wraps the few `tmux` subcommands fanout invokes during
// pane creation. dmuxsession owns show-options / list-sessions; this package
// owns send-keys.
package tmuxctl

import (
	"fmt"
	"io"
	"os/exec"
)

// SendKeys fires named keys at controlPane. In dryRun mode it prints the
// equivalent shell invocation and returns without contacting tmux — matching
// the format fanout:827 emits and that Tier 2 goldens lock in.
func SendKeys(controlPane string, dryRun bool, dimColor, reset string, out io.Writer, key string) error {
	if dryRun {
		fmt.Fprintf(out, "    %s$ tmux send-keys -t %s %s%s\n", dimColor, controlPane, key, reset)
		return nil
	}
	return exec.Command("tmux", "send-keys", "-t", controlPane, key).Run()
}
