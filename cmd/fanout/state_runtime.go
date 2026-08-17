package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const fanoutStatePathEnv = "FANOUT_STATE_PATH"

// invokingPaneEnv is the variable a runtime sets on the process it starts
// inside one of its panes, naming that pane. It is a wire contract with the
// runtime — the pane may have been created by an earlier fanout run — so the
// name is frozen.
const invokingPaneEnv = "TMUX_PANE"

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
	return resolveDisplayProjectRootFrom(top, invokingPaneGitToplevel, projectHasState), nil
}

func resolveDisplayProjectRootFrom(cwdTop string, paneTop func() (string, error), hasState func(string) bool) string {
	cwdRoot := resolveRootFromTop(cwdTop, hasState)
	if hasState(cwdRoot) {
		return cwdRoot
	}
	if paneTop == nil {
		return cwdRoot
	}
	top, err := paneTop()
	if err != nil || strings.TrimSpace(top) == "" {
		return cwdRoot
	}
	paneRoot := resolveRootFromTop(top, hasState)
	if hasState(paneRoot) {
		return paneRoot
	}
	return cwdRoot
}

func projectHasState(dir string) bool {
	return fileExists(state.Path(dir))
}

// invokingPaneGitToplevel resolves the repository root of the pane fanout was
// invoked from. It needs both an environment that names that pane and a runtime
// that reports where a pane sits; either absence leaves the caller with the
// root it resolved from its own process.
func invokingPaneGitToplevel() (string, error) {
	pane := strings.TrimSpace(os.Getenv(invokingPaneEnv))
	if pane == "" {
		return "", fmt.Errorf("the environment does not name an invoking pane")
	}
	locator, ok := backend.AsPaneLocator(paneruntime.NewTmux())
	if !ok {
		return "", fmt.Errorf("the runtime does not report pane locations")
	}
	path, err := locator.PaneCurrentPath(pane)
	if err != nil {
		return "", err
	}
	return gitroot.Toplevel(path)
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
	return gitroot.Toplevel("")
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
