// Package runtime resolves the repository and tmux context fanout runs in.
package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
)

type Info struct {
	Session     string
	Target      string
	ProjectRoot string

	// InvokingPane is the pane fanout itself runs in, as reported by TMUX_PANE.
	// It stays distinct from Target, which --session points at a whole session,
	// and is empty whenever the environment does not name a pane (every herdr
	// run, and any tmux invocation without the variable).
	InvokingPane string
}

// Resolve discovers the git repository root and the selected backend's launch
// context. Only tmux requires an invoking tmux pane; herdr routing is validated
// by the herdr backend against its named session and socket.
func Resolve(name backend.Name, sessionOverride string) (*Info, error) {
	root, err := gitroot.Toplevel("")
	if err != nil {
		return nil, err
	}
	name = backend.NormalizeName(name)
	if _, parseErr := backend.ParseName(string(name)); parseErr != nil {
		return nil, parseErr
	}
	if name == backend.Herdr {
		if sessionOverride != "" {
			return nil, fmt.Errorf("--session is only supported by the tmux backend")
		}
		return &Info{Session: strings.TrimSpace(os.Getenv("HERDR_SESSION")), ProjectRoot: root}, nil
	}
	session, err := tmuxSession()
	if err != nil {
		return nil, err
	}
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if sessionOverride != "" {
		if err = validateTmuxSession(sessionOverride); err != nil {
			return nil, err
		}
		session = sessionOverride
		return &Info{Session: session, Target: sessionOverride, ProjectRoot: root, InvokingPane: pane}, nil
	}
	target, err := invokingPane()
	if err != nil {
		return nil, err
	}
	return &Info{Session: session, Target: target, ProjectRoot: root, InvokingPane: pane}, nil
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
