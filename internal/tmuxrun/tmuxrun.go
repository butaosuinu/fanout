// Package tmuxrun contains the direct tmux operations used by fanout.
package tmuxrun

import (
	"fmt"
	"os/exec"
	"strings"
)

const userShellExpr = `"${SHELL:-/bin/sh}"`

// SplitPane splits the target pane/session rooted at worktreePath and returns its pane id.
func SplitPane(target, worktreePath string) (string, error) {
	return splitPane(target, worktreePath, "")
}

// SplitPaneWithAgentCommand splits the target pane/session and starts the agent
// command through a shell wrapper that keeps the pane alive after the agent exits.
func SplitPaneWithAgentCommand(target, worktreePath, agentCommand string) (string, error) {
	return splitPane(target, worktreePath, BuildPaneLaunchCommand(agentCommand))
}

func splitPane(target, worktreePath, launchCommand string) (string, error) {
	args := []string{"split-window"}
	if target != "" {
		args = append(args, "-t", target)
	}
	args = append(args, "-d", "-h", "-P", "-F", "#{pane_id}", "-c", worktreePath)
	if strings.TrimSpace(launchCommand) != "" {
		args = append(args, launchCommand)
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux split-window: %w", err)
	}
	paneID := strings.TrimSpace(string(out))
	if paneID == "" {
		return "", fmt.Errorf("tmux split-window returned an empty pane id")
	}
	return paneID, nil
}

// BuildPaneLaunchCommand returns a tmux shell-command that starts the agent via
// a POSIX wrapper and leaves the user's shell behind after the agent exits.
func BuildPaneLaunchCommand(agentCommand string) string {
	agentCommand = strings.TrimSpace(agentCommand)
	if agentCommand == "" {
		return ""
	}
	body := agentCommand + `; __fanout_status=$?; printf '\n[fanout] agent exited with status %d; returning to shell.\n' "$__fanout_status"; exec ` + userShellExpr + ` -l`
	return "exec /bin/sh -lc " + shellQuote(body)
}

// SelectTiled applies tmux's tiled layout to the target pane/session.
func SelectTiled(session string) error {
	args := []string{"select-layout"}
	if session != "" {
		args = append(args, "-t", session)
	}
	args = append(args, "tiled")
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux select-layout tiled: %w", err)
	}
	return nil
}

// CapturePane returns the currently visible text for a pane.
func CapturePane(paneID string) (string, error) {
	if strings.TrimSpace(paneID) == "" {
		return "", fmt.Errorf("tmux capture-pane: empty pane id")
	}

	out, altErr := capturePane(paneID, "-a")
	if altErr == nil {
		return out, nil
	}

	out, err := capturePane(paneID)
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: alternate screen: %v; normal screen: %w", altErr, err)
	}
	return out, nil
}

func capturePane(paneID string, extraArgs ...string) (string, error) {
	args := []string{"capture-pane", "-t", paneID, "-p"}
	args = append(args, extraArgs...)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SendLiteral sends text to a pane without tmux interpreting it as key names.
func SendLiteral(paneID, text string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("tmux send-keys: empty pane id")
	}
	if err := exec.Command("tmux", "send-keys", "-t", paneID, "-l", text).Run(); err != nil {
		return fmt.Errorf("tmux send-keys -l: %w", err)
	}
	return nil
}

// SendKey sends a named tmux key, such as C-m, to a pane.
func SendKey(paneID, key string) error {
	if strings.TrimSpace(paneID) == "" {
		return fmt.Errorf("tmux send-keys: empty pane id")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("tmux send-keys: empty key")
	}
	if err := exec.Command("tmux", "send-keys", "-t", paneID, key).Run(); err != nil {
		return fmt.Errorf("tmux send-keys %s: %w", key, err)
	}
	return nil
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == ':' || r == '.' || r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// SetPaneTitle sets the tmux pane title when a display name is supplied.
func SetPaneTitle(paneID, title string) error {
	if strings.TrimSpace(title) == "" {
		return nil
	}
	if err := exec.Command("tmux", "select-pane", "-t", paneID, "-T", title).Run(); err != nil {
		return fmt.Errorf("tmux select-pane -T: %w", err)
	}
	return nil
}

// KillPane closes a pane created during a failed launch attempt.
func KillPane(paneID string) error {
	if strings.TrimSpace(paneID) == "" {
		return nil
	}
	if err := exec.Command("tmux", "kill-pane", "-t", paneID).Run(); err != nil {
		return fmt.Errorf("tmux kill-pane: %w", err)
	}
	return nil
}
