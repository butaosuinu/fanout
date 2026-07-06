package tmuxrun

import (
	"fmt"
	"os/exec"
	"strings"
)

// PaneCurrentPath returns the pane's current working directory
// (#{pane_current_path}) via `tmux display-message -p -t <paneID>`.
func PaneCurrentPath(paneID string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{pane_current_path}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message pane_current_path: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("tmux pane current path is empty")
	}
	return path, nil
}
