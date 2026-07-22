// Package agent builds validated launch commands for supported coding agents.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// LaunchMode selects the agent's initial permission posture.
type LaunchMode string

const (
	ModeBuild LaunchMode = "build"
	ModePlan  LaunchMode = "plan"
)

type Definition struct {
	Name    string
	Command string
	// LaunchArgs are flags injected into every launch and resume command.
	LaunchArgs []string
	// BackendLaunchArgs are flags injected only for the selected runtime.
	// Claude's tmux entry carries the @fanout_agent_state hook settings;
	// non-tmux runtimes must not receive commands that target tmux pane state.
	BackendLaunchArgs map[backend.Name][]string
	// ModeArgs are flags injected only into launches with an explicit mode.
	// Resume commands leave the restored agent's current posture unchanged.
	ModeArgs map[LaunchMode][]string
	// PromptFlag, when non-empty, passes the launch prompt as this flag's value
	// instead of a positional argument (opencode's positional is a project
	// path). Emitted only when a prompt is present, so resume commands never
	// receive a value-less flag.
	PromptFlag string
	ResumeArgs []string
}

var registry = map[string]Definition{
	"claude": {
		Name:              "claude",
		Command:           "claude",
		BackendLaunchArgs: map[backend.Name][]string{backend.Tmux: {"--settings", claudeHookSettingsJSON}},
		ModeArgs: map[LaunchMode][]string{
			ModeBuild: {"--permission-mode", "auto"},
			ModePlan:  {"--permission-mode", "plan"},
		},
		ResumeArgs: []string{"--continue"},
	},
	"codex": {Name: "codex", Command: "codex", ResumeArgs: []string{"resume", "--last"}},
	"opencode": {
		Name:    "opencode",
		Command: "opencode",
		ModeArgs: map[LaunchMode][]string{
			ModeBuild: {"--agent", "build"},
			ModePlan:  {"--agent", "plan"},
		},
		PromptFlag: "--prompt",
		ResumeArgs: []string{"--continue"},
	},
}

// ValidateKnown returns an error if name is not in fanout's MVP registry.
func ValidateKnown(name string) error {
	if _, ok := registry[name]; ok {
		return nil
	}
	return fmt.Errorf("unknown agent %q (supported: %s)", name, strings.Join(Supported(), ", "))
}

// ResolveExecutable returns the absolute executable path found in the caller's
// environment. tmux panes may not inherit PATH changes made after the tmux
// server started, so live launches send this path instead of the bare command.
func ResolveExecutable(name string) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	path, err := exec.LookPath(def.Command)
	if err != nil {
		return "", fmt.Errorf("agent %q is not installed or not on PATH (missing command: %s)", name, def.Command)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve agent %q executable path: %w", name, err)
		}
	}
	return path, nil
}

// ValidateInstalled returns an error if the agent command is not on PATH.
func ValidateInstalled(name string) error {
	_, err := ResolveExecutable(name)
	return err
}

// BuildCommand returns the shell command sent to a tmux child pane.
func BuildCommand(name, prompt string) (string, error) {
	return BuildCommandForBackend(name, prompt, backend.Tmux)
}

// BuildCommandWithMode returns the shell command sent to a tmux child pane
// with the agent's initial launch mode made explicit.
func BuildCommandWithMode(name, prompt string, mode LaunchMode) (string, error) {
	return BuildCommandForBackendWithMode(name, prompt, backend.Tmux, mode)
}

// BuildCommandForBackend returns the shell command for the selected runtime.
func BuildCommandForBackend(name, prompt string, runtimeBackend backend.Name) (string, error) {
	return BuildCommandForBackendWithMode(name, prompt, runtimeBackend, "")
}

// BuildCommandForBackendWithMode returns the shell command for the selected
// runtime with the agent's initial launch mode made explicit.
func BuildCommandForBackendWithMode(name, prompt string, runtimeBackend backend.Name, mode LaunchMode) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	args, err := launchArgsForBackend(def, runtimeBackend, mode)
	if err != nil {
		return "", err
	}
	return buildCommand(def.Command, args, def.PromptFlag, prompt), nil
}

// BuildResolvedCommand returns the live-launch command using the resolved
// executable path from the caller's environment.
func BuildResolvedCommand(name, prompt string) (string, error) {
	return BuildResolvedCommandForBackend(name, prompt, backend.Tmux)
}

// BuildResolvedCommandWithMode returns the live-launch command using the
// resolved executable path with the agent's initial launch mode made explicit.
func BuildResolvedCommandWithMode(name, prompt string, mode LaunchMode) (string, error) {
	return BuildResolvedCommandForBackendWithMode(name, prompt, backend.Tmux, mode)
}

// BuildResolvedCommandForBackend returns the live-launch command for the
// selected runtime using the resolved executable path from the caller's
// environment.
func BuildResolvedCommandForBackend(name, prompt string, runtimeBackend backend.Name) (string, error) {
	return BuildResolvedCommandForBackendWithMode(name, prompt, runtimeBackend, "")
}

// BuildResolvedCommandForBackendWithMode returns the live-launch command for
// the selected runtime using the resolved executable path with the agent's
// initial launch mode made explicit.
func BuildResolvedCommandForBackendWithMode(name, prompt string, runtimeBackend backend.Name, mode LaunchMode) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	args, err := launchArgsForBackend(def, runtimeBackend, mode)
	if err != nil {
		return "", err
	}
	path, err := ResolveExecutable(name)
	if err != nil {
		return "", err
	}
	return "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + buildCommand(path, args, def.PromptFlag, prompt), nil
}

// BuildResumeCommand returns the generic resume command for a supported agent.
func BuildResumeCommand(name string) (string, error) {
	return BuildResumeCommandForBackend(name, backend.Tmux)
}

// BuildResumeCommandForBackend returns the resume command for the selected
// runtime.
func BuildResumeCommandForBackend(name string, runtimeBackend backend.Name) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	args, err := launchArgsForBackend(def, runtimeBackend, "")
	if err != nil {
		return "", err
	}
	return buildCommand(def.Command, slices.Concat(args, def.ResumeArgs), def.PromptFlag, ""), nil
}

// BuildResolvedResumeCommand returns the live resume command using the resolved
// executable path from the caller's environment.
func BuildResolvedResumeCommand(name string) (string, error) {
	return BuildResolvedResumeCommandForBackend(name, backend.Tmux)
}

// BuildResolvedResumeCommandForBackend returns the live resume command for the
// selected runtime using the resolved executable path from the caller's
// environment.
func BuildResolvedResumeCommandForBackend(name string, runtimeBackend backend.Name) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	args, err := launchArgsForBackend(def, runtimeBackend, "")
	if err != nil {
		return "", err
	}
	path, err := ResolveExecutable(name)
	if err != nil {
		return "", err
	}
	return "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + buildCommand(path, slices.Concat(args, def.ResumeArgs), def.PromptFlag, ""), nil
}

func launchArgsForBackend(def Definition, runtimeBackend backend.Name, mode LaunchMode) ([]string, error) {
	name, err := backend.ParseName(string(runtimeBackend))
	if err != nil {
		return nil, err
	}
	return slices.Concat(def.LaunchArgs, def.BackendLaunchArgs[name], def.ModeArgs[mode]), nil
}

// WithFanoutBin pins helper calls made by the launched agent to the same fanout
// executable that created its pane.
func WithFanoutBin(command, fanoutPath string) string {
	return "FANOUT_BIN=" + ShellQuote(fanoutPath) + " " + command
}

// buildCommand assembles a shell command from an executable, per-agent flags,
// and an optional prompt, quoting every token. promptFlag routes a non-empty
// prompt through that flag (e.g. opencode --prompt) instead of a positional.
func buildCommand(executable string, args []string, promptFlag, prompt string) string {
	parts := []string{ShellQuote(executable)}
	for _, arg := range args {
		parts = append(parts, ShellQuote(arg))
	}
	if strings.TrimSpace(prompt) != "" {
		if promptFlag != "" {
			parts = append(parts, ShellQuote(promptFlag))
		}
		parts = append(parts, ShellQuote(prompt))
	}
	return strings.Join(parts, " ")
}

func Supported() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// PaneStateRefined reports whether the agent refines @fanout_agent_state
// beyond the launch wrapper's running/done — claude via its tmux hooks and
// codex via the Plan Mode controller and team bridge. A pane of an agent
// without refinement stays "running" even while a permission dialog is
// focused, so peermsg must not send-keys a nudge into it. Unknown and empty
// names fail safe: no nudge.
func PaneStateRefined(name string) bool {
	switch name {
	case "claude", "codex":
		return true
	default:
		return false
	}
}

// ShellQuote quotes one POSIX shell token.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r != '/' && r != ':' && r != '.' && r != '-' && r != '_' && (r < '0' || r > '9') && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
