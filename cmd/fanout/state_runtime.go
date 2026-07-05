package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const fanoutStatePathEnv = "FANOUT_STATE_PATH"

type fanoutStateRuntime struct {
	projectRoot string
	statePath   string
}

func resolveStateRuntimeForMode(mode string, lg *log.Logger) (fanoutStateRuntime, exitcode.Code) {
	rt, err := resolveStateRuntime()
	if err != nil {
		lg.Err("%s: %v", mode, err)
		if mode == "--status" {
			return fanoutStateRuntime{}, exitcode.Invocation
		}
		return fanoutStateRuntime{}, exitcode.Env
	}
	return rt, exitcode.OK
}

func resolveStateRuntime() (fanoutStateRuntime, error) {
	if raw := os.Getenv(fanoutStatePathEnv); raw != "" {
		path, err := filepath.Abs(raw)
		if err != nil {
			path = raw
		}
		return fanoutStateRuntime{
			projectRoot: inferProjectRootFromStatePath(path),
			statePath:   path,
		}, nil
	}
	root, err := gitToplevelFromCwd()
	if err != nil {
		return fanoutStateRuntime{}, err
	}
	return fanoutStateRuntime{projectRoot: root, statePath: state.Path(root)}, nil
}

// resolveDisplayProjectRoot resolves the project root whose fanout Session
// state should be displayed by dashboard-style surfaces (the resident TUI and
// web dashboard). Unlike action/status modes, display commands are often launched
// from Codex/dmux wrappers whose process cwd can differ from the tmux pane's
// visible current path. Prefer a conventional FANOUT_STATE_PATH
// (<root>/.fanout/state.json), then the cwd root when it already owns state,
// then the tmux pane path when that is the only state-owning root available.
func resolveDisplayProjectRoot() (string, error) {
	if raw := os.Getenv(fanoutStatePathEnv); raw != "" {
		path, err := filepath.Abs(raw)
		if err != nil {
			path = raw
		}
		return inferProjectRootFromStatePath(path), nil
	}
	top, err := gitToplevelFromCwd()
	if err != nil {
		return "", err
	}
	return resolveDisplayProjectRootFrom(top, tmuxPaneGitToplevel, projectHasState), nil
}

func resolveDisplayProjectRootFrom(cwdTop string, tmuxTop func() (string, error), hasState func(string) bool) string {
	cwdRoot := resolveRootFromTop(cwdTop, hasState)
	if hasState(cwdRoot) {
		return cwdRoot
	}
	if tmuxTop == nil {
		return cwdRoot
	}
	top, err := tmuxTop()
	if err != nil || strings.TrimSpace(top) == "" {
		return cwdRoot
	}
	tmuxRoot := resolveRootFromTop(top, hasState)
	if hasState(tmuxRoot) {
		return tmuxRoot
	}
	return cwdRoot
}

func projectHasState(dir string) bool {
	return fileExists(state.Path(dir))
}

func tmuxPaneGitToplevel() (string, error) {
	pane := strings.TrimSpace(os.Getenv("TMUX_PANE"))
	if pane == "" {
		return "", fmt.Errorf("not inside a tmux pane")
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_current_path}").Output()
	if err != nil {
		return "", fmt.Errorf("tmux display-message pane_current_path: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("tmux pane current path is empty")
	}
	return gitToplevelAt(path)
}

func inferProjectRootFromStatePath(path string) string {
	stateDir := filepath.Dir(path)
	if filepath.Base(stateDir) == ".fanout" {
		return filepath.Dir(stateDir)
	}
	if root, err := gitToplevelFromCwd(); err == nil {
		return root
	}
	return filepath.Dir(filepath.Dir(path))
}

func gitToplevelFromCwd() (string, error) {
	return gitToplevelAt("")
}

func gitToplevelAt(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	return root, nil
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func emptyLabel(s string) string {
	if s == "" {
		return "<empty>"
	}
	return s
}
