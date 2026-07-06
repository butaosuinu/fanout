// Package agent builds validated launch commands for supported coding agents.
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type Definition struct {
	Name    string
	Command string
	// LaunchArgs are flags injected into every launch and resume command.
	// claude carries the @fanout_agent_state hook settings; codex has no
	// launch-time hook mechanism and keeps the bare two-value wrapper signal.
	LaunchArgs []string
	ResumeArgs []string
}

var registry = map[string]Definition{
	"claude": {Name: "claude", Command: "claude", LaunchArgs: []string{"--settings", claudeHookSettingsJSON}, ResumeArgs: []string{"--continue"}},
	"codex":  {Name: "codex", Command: "codex", ResumeArgs: []string{"resume", "--last"}},
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

// BuildCommand returns the shell command sent to the tmux child pane.
func BuildCommand(name, prompt string) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	return buildCommand(def.Command, def.LaunchArgs, prompt), nil
}

// BuildResolvedCommand returns the live-launch command using the resolved
// executable path from the caller's environment.
func BuildResolvedCommand(name, prompt string) (string, error) {
	path, err := ResolveExecutable(name)
	if err != nil {
		return "", err
	}
	return "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + buildCommand(path, registry[name].LaunchArgs, prompt), nil
}

// BuildResumeCommand returns the generic resume command for a supported agent.
func BuildResumeCommand(name string) (string, error) {
	def, ok := registry[name]
	if !ok {
		return "", ValidateKnown(name)
	}
	return buildCommand(def.Command, slices.Concat(def.LaunchArgs, def.ResumeArgs), ""), nil
}

// BuildResolvedResumeCommand returns the live resume command using the resolved
// executable path from the caller's environment.
func BuildResolvedResumeCommand(name string) (string, error) {
	path, err := ResolveExecutable(name)
	if err != nil {
		return "", err
	}
	def := registry[name]
	return "PATH=" + ShellQuote(os.Getenv("PATH")) + " " + buildCommand(path, slices.Concat(def.LaunchArgs, def.ResumeArgs), ""), nil
}

// buildCommand assembles a shell command from an executable, per-agent flags,
// and an optional prompt, quoting every token.
func buildCommand(executable string, args []string, prompt string) string {
	parts := []string{ShellQuote(executable)}
	for _, arg := range args {
		parts = append(parts, ShellQuote(arg))
	}
	if strings.TrimSpace(prompt) != "" {
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
