// Package runtime resolves the repository and tmux context fanout runs in.
package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Info struct {
	Session     string
	Target      string
	ProjectRoot string
}

// Resolve discovers the git repository root and tmux target fanout should split.
func Resolve(sessionOverride string) (*Info, error) {
	root, err := gitToplevel()
	if err != nil {
		return nil, err
	}
	session, err := tmuxSession()
	if err != nil {
		return nil, err
	}
	if sessionOverride != "" {
		if err = validateTmuxSession(sessionOverride); err != nil {
			return nil, err
		}
		session = sessionOverride
		return &Info{Session: session, Target: sessionOverride, ProjectRoot: root}, nil
	}
	target, err := invokingPane()
	if err != nil {
		return nil, err
	}
	return &Info{Session: session, Target: target, ProjectRoot: root}, nil
}

func gitToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	return root, nil
}

func tmuxSession() (string, error) {
	if os.Getenv("TMUX") == "" {
		return "", fmt.Errorf("fanout must be run inside tmux; start or attach a tmux session first")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("could not resolve current tmux session: %w", err)
	}
	session := strings.TrimSpace(string(out))
	if session == "" {
		return "", fmt.Errorf("tmux did not report a current session name")
	}
	return session, nil
}

func invokingPane() (string, error) {
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return pane, nil
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{pane_id}").Output()
	if err != nil {
		return "", fmt.Errorf("could not resolve invoking tmux pane: %w", err)
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		return "", fmt.Errorf("tmux did not report an invoking pane id")
	}
	return pane, nil
}

func validateTmuxSession(session string) error {
	out, err := exec.Command("tmux", "has-session", "-t", session).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg != "" {
		return fmt.Errorf("tmux session %q is not available: %s", session, msg)
	}
	return fmt.Errorf("tmux session %q is not available", session)
}
