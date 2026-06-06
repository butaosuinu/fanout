// Package tmuxrun contains the direct tmux operations used by fanout.
package tmuxrun

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const userShellExpr = `"${SHELL:-/bin/sh}"`
const paneListFormat = "#{pane_id}:#{window_id}:#{pane_index}:#{pane_active}:#{pane_title}"

// PaneInfo describes a pane currently known to tmux.
type PaneInfo struct {
	ID       string
	WindowID string
	Index    int
	Active   bool
	Title    string
}

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

// ListPanes returns pane metadata for a target pane, window, or session.
func ListPanes(target string) ([]PaneInfo, error) {
	target = strings.TrimSpace(target)
	args := []string{"list-panes"}
	if target != "" {
		if shouldListSessionPanes(target) {
			args = append(args, "-s")
			target = exactSessionTarget(target)
		}
		args = append(args, "-t", target)
	}
	args = append(args, "-F", paneListFormat)
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	return parseListPanesOutput(string(out))
}

func shouldListSessionPanes(target string) bool {
	return HasSession(target)
}

func exactSessionTarget(name string) string {
	return "=" + name
}

func parseListPanesOutput(out string) ([]PaneInfo, error) {
	var panes []PaneInfo
	for lineNum, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, ":", 5)
		if len(fields) != 5 {
			return nil, fmt.Errorf("parse tmux list-panes line %d: expected 5 fields, got %d", lineNum+1, len(fields))
		}
		index, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse tmux list-panes line %d index: %w", lineNum+1, err)
		}
		active, err := parsePaneActive(fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse tmux list-panes line %d active: %w", lineNum+1, err)
		}
		panes = append(panes, PaneInfo{
			ID:       fields[0],
			WindowID: fields[1],
			Index:    index,
			Active:   active,
			Title:    fields[4],
		})
	}
	return panes, nil
}

func parsePaneActive(raw string) (bool, error) {
	switch raw {
	case "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, fmt.Errorf("expected 0 or 1, got %q", raw)
	}
}

// CapturePaneOutput returns a read-only output snapshot from a tmux pane.
func CapturePaneOutput(paneID string, lines int) (string, error) {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return "", fmt.Errorf("pane id is required")
	}
	if lines < 0 {
		return "", fmt.Errorf("lines must be non-negative")
	}
	if out, err := capturePaneOutput(paneID, 0, true); err == nil {
		return out, nil
	}
	return capturePaneOutput(paneID, lines, false)
}

func capturePaneOutput(paneID string, lines int, alternateScreen bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", paneID}
	if alternateScreen {
		args = []string{"capture-pane", "-a", "-p", "-t", paneID}
	}
	if !alternateScreen && lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// SelectPane selects paneID within its containing tmux window.
func SelectPane(paneID string) error {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return fmt.Errorf("pane id is required")
	}
	if err := exec.Command("tmux", "select-pane", "-t", paneID).Run(); err != nil {
		return fmt.Errorf("tmux select-pane: %w", err)
	}
	return nil
}

// IsPaneAlive reports whether tmux can resolve the pane id.
func IsPaneAlive(paneID string) bool {
	paneID = strings.TrimSpace(paneID)
	if paneID == "" {
		return false
	}
	return exec.Command("tmux", "display-message", "-p", "-t", paneID).Run() == nil
}

// InsideTmux reports whether the current process is running under tmux.
func InsideTmux() bool {
	return strings.TrimSpace(os.Getenv("TMUX")) != ""
}

// HasSession reports whether a tmux session exists.
func HasSession(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	return exec.Command("tmux", "has-session", "-t", exactSessionTarget(name)).Run() == nil
}

// NewSession creates a detached tmux session rooted at startDir.
func NewSession(name, startDir string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	args := []string{"new-session", "-d", "-s", name}
	if strings.TrimSpace(startDir) != "" {
		args = append(args, "-c", startDir)
	}
	if err := exec.Command("tmux", args...).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	return nil
}

// AttachOrSwitch attaches to a session outside tmux or switches clients inside tmux.
func AttachOrSwitch(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	target := exactSessionTarget(name)
	insideTmux := InsideTmux()
	args := []string{"attach-session", "-t", target}
	if insideTmux {
		args = []string{"switch-client", "-t", target}
	}
	cmd := exec.Command("tmux", args...)
	if !insideTmux {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux %s: %w", args[0], err)
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
