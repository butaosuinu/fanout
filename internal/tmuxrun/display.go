package tmuxrun

import (
	"fmt"
	"os/exec"
	"strings"
)

// DisplayMessage shows msg in tmux's status line for the current client or the
// supplied target.
func DisplayMessage(target, msg string) error {
	var targetArgs []string
	if trimmed := strings.TrimSpace(target); trimmed != "" {
		targetArgs = []string{"-t", exactSessionTarget(trimmed)}
	}
	return displayMessage(targetArgs, msg)
}

// DisplayMessageToClient shows msg on the status line of one client — for
// keybinding-driven commands, the client that pressed the key (#{client_name}
// expanded at keypress). An empty client falls back to tmux's current-client
// resolution, which outside a client context is the most recently active one.
func DisplayMessageToClient(client, msg string) error {
	var targetArgs []string
	if strings.TrimSpace(client) != "" {
		targetArgs = []string{"-c", client}
	}
	return displayMessage(targetArgs, msg)
}

func displayMessage(targetArgs []string, msg string) error {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return fmt.Errorf("message is required")
	}
	args := append([]string{"display-message"}, targetArgs...)
	args = append(args, tmuxLiteral(msg))
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux display-message: %w", err)
	}
	return nil
}

// tmuxLiteral escapes '#' so tmux format expansion treats msg as literal text.
func tmuxLiteral(msg string) string {
	return strings.ReplaceAll(msg, "#", "##")
}
