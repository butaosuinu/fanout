// Package dmuxsession resolves which tmux session is running dmux. Mirrors
// fanout:411-482.
package dmuxsession

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type Info struct {
	Session     string
	ControlPane string
	ConfigPath  string
	ProjectRoot string
}

// TmuxOption returns the value of `tmux show-options -v -t <session> <key>`
// or "" when unset.
func TmuxOption(session, key string) string {
	out, err := exec.Command("tmux", "show-options", "-v", "-t", session, key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// ListSessions returns the names of every tmux session, or an error if no
// tmux server is running.
func ListSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, fmt.Errorf("no tmux server is running. Start dmux in your repo first: cd <repo> && dmux")
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}

func pidAlive(pidStr string) bool {
	if pidStr == "" {
		return false
	}
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// IsDmux reports whether session has @dmux_controller_pid pointing at a live
// process.
func IsDmux(session string) bool {
	return pidAlive(TmuxOption(session, "@dmux_controller_pid"))
}

// Resolve picks the dmux session given an optional --session preference.
// Returns an error matching the bash die() messages when 0 or many sessions
// are dmux-managed and no preference disambiguates.
func Resolve(sessionArg string) (*Info, error) {
	sessions, err := ListSessions()
	if err != nil {
		return nil, err
	}

	var dmuxSessions []string
	for _, s := range sessions {
		if IsDmux(s) {
			dmuxSessions = append(dmuxSessions, s)
		}
	}

	if len(dmuxSessions) == 0 {
		return nil, fmt.Errorf("no active dmux session found. Start one with: cd <repo> && dmux")
	}

	var session string
	switch {
	case sessionArg != "":
		found := false
		for _, s := range dmuxSessions {
			if s == sessionArg {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("tmux session '%s' is not running dmux. Active dmux sessions: %s", sessionArg, strings.Join(dmuxSessions, " "))
		}
		session = sessionArg
	case len(dmuxSessions) > 1:
		return nil, fmt.Errorf("multiple dmux sessions active (%s); pass --session <name> to pick one", strings.Join(dmuxSessions, " "))
	default:
		session = dmuxSessions[0]
	}

	info := &Info{
		Session:     session,
		ControlPane: TmuxOption(session, "@dmux_control_pane"),
		ConfigPath:  TmuxOption(session, "@dmux_config_path"),
		ProjectRoot: TmuxOption(session, "@dmux_project_root"),
	}
	if info.ControlPane == "" {
		return nil, fmt.Errorf("session '%s' has no @dmux_control_pane option; dmux may be in a broken state", session)
	}
	if info.ConfigPath == "" {
		return nil, fmt.Errorf("session '%s' has no @dmux_config_path option", session)
	}
	if info.ProjectRoot == "" {
		return nil, fmt.Errorf("session '%s' has no @dmux_project_root option", session)
	}
	return info, nil
}
