package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/state"
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
