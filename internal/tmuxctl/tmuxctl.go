// Package tmuxctl contains small tmux control-plane operations that are not
// tied to launching or focusing fanout panes.
package tmuxctl

import (
	"fmt"
	"os/exec"
	"strings"
)

// DisplayMessage shows msg in tmux's status line for the current client or the
// supplied target.
func DisplayMessage(target, msg string) error {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Errorf("message is required")
	}
	args := []string{"display-message"}
	if strings.TrimSpace(target) != "" {
		args = append(args, "-t", exactTarget(target))
	}
	args = append(args, tmuxLiteral(msg))
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux display-message: %w", err)
	}
	return nil
}

func tmuxLiteral(msg string) string {
	return strings.ReplaceAll(msg, "#", "##")
}

func exactTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" || strings.HasPrefix(target, "=") || strings.HasPrefix(target, "$") || strings.HasPrefix(target, "%") || strings.ContainsAny(target, "*?[") {
		return target
	}
	return "=" + target
}
