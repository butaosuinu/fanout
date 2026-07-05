// Package hooks runs fanout lifecycle hooks from user config.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	userConfigRelPath = "fanout/hooks.json"
	defaultTimeout    = 60 * time.Second

	backgroundRunnerCommand = "__fanout-hook-runner"
)

// Type names a lifecycle hook event.
type Type string

const (
	BeforePaneCreate     Type = "before_pane_create"
	WorktreeCreated      Type = "worktree_created"
	BeforePaneClose      Type = "before_pane_close"
	PaneClosed           Type = "pane_closed"
	BeforeWorktreeRemove Type = "before_worktree_remove"
	WorktreeRemoved      Type = "worktree_removed"
	PreMerge             Type = "pre_merge"
	PostMerge            Type = "post_merge"
)

// Context is exposed to hook processes through FANOUT_* and compatibility
// DMUX_* environment variables.
type Context struct {
	ProjectRoot  string
	Parent       string
	IssueNum     int
	TaskID       string
	Slug         string
	Prompt       string
	Agent        string
	TmuxPaneID   string
	WorktreePath string
	Branch       string
	BaseBranch   string
	TargetBranch string
}

// Logger is the narrow logging surface hook execution needs.
type Logger interface {
	Warn(format string, a ...any)
	Err(format string, a ...any)
	Stderr() io.Writer
}

// Config contains command hooks loaded from the user config file.
type Config struct {
	Path   string
	Events map[Type][]Command
}

// Command is one configured shell command hook.
type Command struct {
	Command       string
	Timeout       time.Duration
	StatusMessage string
}

// Result describes one blocking hook invocation.
type Result struct {
	Ran     bool
	Command string
	Output  []byte
	Err     error
}

// OK reports whether the hook was skipped or ran successfully.
func (r Result) OK() bool {
	return r.Err == nil
}

// EmptyConfig returns a config with no hook commands.
func EmptyConfig() Config {
	return Config{Events: map[Type][]Command{}}
}

// UserConfigPath returns the user-managed fanout hook config path.
func UserConfigPath() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, userConfigRelPath)
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", userConfigRelPath)
	}
	return ""
}

// LoadUserConfig loads Codex-style command hooks from the user config file.
func LoadUserConfig(lg Logger) Config {
	path := UserConfigPath()
	cfg := EmptyConfig()
	cfg.Path = path
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			warnf(lg, "hooks config %s: read failed: %v (ignored)", path, err)
		}
		return cfg
	}

	var file configFile
	if err := json.Unmarshal(data, &file); err != nil {
		warnf(lg, "hooks config %s: parse failed: %v (ignored)", path, err)
		return cfg
	}
	for rawEvent, groups := range file.Hooks {
		hook, ok := parseType(rawEvent)
		if !ok {
			warnf(lg, "hooks config %s: unknown event %q (ignored)", path, rawEvent)
			continue
		}
		for groupIdx, group := range groups {
			for hookIdx, entry := range group.Hooks {
				cmd, ok := parseCommand(path, rawEvent, groupIdx, hookIdx, entry, lg)
				if ok {
					cfg.Events[hook] = append(cfg.Events[hook], cmd)
				}
			}
		}
	}
	return cfg
}

type configFile struct {
	Hooks map[string][]configGroup `json:"hooks"`
}

type configGroup struct {
	Hooks []configHook `json:"hooks"`
}

type configHook struct {
	Type          string   `json:"type"`
	Command       string   `json:"command"`
	Timeout       *float64 `json:"timeout"`
	StatusMessage string   `json:"statusMessage"`
}

func parseCommand(path, event string, groupIdx, hookIdx int, entry configHook, lg Logger) (Command, bool) {
	label := fmt.Sprintf("%s hooks.%s[%d].hooks[%d]", path, event, groupIdx, hookIdx)
	if entry.Type != "command" {
		warnf(lg, "hooks config %s: unsupported type %q (ignored)", label, entry.Type)
		return Command{}, false
	}
	command := strings.TrimSpace(entry.Command)
	if command == "" {
		warnf(lg, "hooks config %s: command must not be empty (ignored)", label)
		return Command{}, false
	}
	timeout := defaultTimeout
	if entry.Timeout != nil {
		if *entry.Timeout <= 0 {
			warnf(lg, "hooks config %s: timeout must be positive seconds (ignored)", label)
			return Command{}, false
		}
		timeout = time.Duration(*entry.Timeout * float64(time.Second))
	}
	return Command{Command: command, Timeout: timeout, StatusMessage: strings.TrimSpace(entry.StatusMessage)}, true
}

// RunBackground starts hook commands asynchronously.
func RunBackground(hook Type, ctx Context, cfg Config, lg Logger) Result {
	commands := cfg.Events[hook]
	if len(commands) == 0 {
		return Result{}
	}
	if err := startBackgroundRunner(hook, commands, ctx); err != nil {
		warnf(lg, "%s hook runner failed: %v", hook, err)
		return Result{Ran: true, Err: err}
	}
	return Result{Ran: true}
}

// IsBackgroundRunnerRequest reports whether args target fanout's hidden hook
// runner process.
func IsBackgroundRunnerRequest(args []string) bool {
	return len(args) > 0 && args[0] == backgroundRunnerCommand
}

// RunBackgroundRunner runs a serialized background hook payload written by
// RunBackground. It is invoked by the fanout binary through a hidden command.
func RunBackgroundRunner(args []string, errw io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(errw, "fanout hook runner: missing payload path")
		return 2
	}
	payloadPath := args[0]
	defer func() {
		// The payload is temporary; failure only leaves an inert file in /tmp.
		_ = os.Remove(payloadPath)
	}()
	data, err := os.ReadFile(payloadPath)
	if err != nil {
		fmt.Fprintf(errw, "fanout hook runner: read %s: %v\n", payloadPath, err)
		return 1
	}
	var payload backgroundPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		fmt.Fprintf(errw, "fanout hook runner: parse %s: %v\n", payloadPath, err)
		return 1
	}
	if runBackgroundCommands(payload.Hook, payload.Commands, payload.Context, runnerLogger{w: errw}) {
		return 0
	}
	return 1
}

type backgroundPayload struct {
	Hook     Type      `json:"hook"`
	Context  Context   `json:"context"`
	Commands []Command `json:"commands"`
}

type runnerLogger struct {
	w io.Writer
}

func (l runnerLogger) Warn(format string, a ...any) {
	fmt.Fprintf(l.w, "[warn] "+format+"\n", a...)
}

func (l runnerLogger) Err(format string, a ...any) {
	fmt.Fprintf(l.w, "[err ] "+format+"\n", a...)
}

func (l runnerLogger) Stderr() io.Writer {
	return l.w
}

func startBackgroundRunner(hook Type, commands []Command, ctx Context) error {
	payload, err := json.Marshal(backgroundPayload{
		Hook:     hook,
		Context:  ctx,
		Commands: commands,
	})
	if err != nil {
		return fmt.Errorf("marshal background hook payload: %w", err)
	}
	file, err := os.CreateTemp("", "fanout-hook-*.json")
	if err != nil {
		return fmt.Errorf("create background hook payload: %w", err)
	}
	payloadPath := file.Name()
	cleanup := true
	defer func() {
		if cleanup {
			// If the runner did not start, no process owns this payload.
			_ = os.Remove(payloadPath)
		}
	}()
	if _, writeErr := file.Write(payload); writeErr != nil {
		// A failed payload write means no runner can consume this file.
		_ = file.Close()
		return fmt.Errorf("write background hook payload: %w", writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close background hook payload: %w", closeErr)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve fanout executable for background hook: %w", err)
	}
	cmd := exec.Command(exe, backgroundRunnerCommand, payloadPath)
	cmd.Dir = ctx.ProjectRoot
	cmd.Env = Env(ctx)
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background hook runner: %w", err)
	}
	cleanup = false
	go func() {
		// The helper is detached; foreground commands cannot use its exit status.
		_ = cmd.Wait()
	}()
	return nil
}

func runBackgroundCommands(hook Type, commands []Command, ctx Context, lg Logger) bool {
	for _, hookCmd := range commands {
		if hookCmd.StatusMessage != "" {
			warnf(lg, "%s", hookCmd.StatusMessage)
		}
		if out, err := runCommand(hookCmd, ctx); err != nil {
			warnf(lg, "%s hook command failed: %v", hook, err)
			if s := strings.TrimSpace(string(out)); s != "" {
				warnf(lg, "%s hook command output: %s", hook, s)
			}
			return false
		}
	}
	return true
}

// RunBlocking runs hook commands synchronously and returns captured output.
func RunBlocking(hook Type, ctx Context, cfg Config, lg Logger) Result {
	commands := cfg.Events[hook]
	if len(commands) == 0 {
		return Result{}
	}
	var combined []byte
	for _, hookCmd := range commands {
		if hookCmd.StatusMessage != "" {
			warnf(lg, "%s", hookCmd.StatusMessage)
		}
		out, err := runCommand(hookCmd, ctx)
		combined = append(combined, out...)
		if err != nil {
			return Result{
				Ran:     true,
				Command: hookCmd.Command,
				Output:  combined,
				Err:     fmt.Errorf("%s hook command failed: %w", hook, err),
			}
		}
	}
	return Result{Ran: true, Output: combined}
}

func runCommand(hookCmd Command, ctx Context) ([]byte, error) {
	timeout := effectiveTimeout(hookCmd)
	cmd := hookCommand(hookCmd.Command, ctx)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	setProcessGroup(cmd)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return out.Bytes(), err
	case sig := <-sigCh:
		killProcessGroup(cmd.Process.Pid)
		<-done
		return out.Bytes(), fmt.Errorf("interrupted by %s", sig)
	case <-timer.C:
		killProcessGroup(cmd.Process.Pid)
		<-done
		return out.Bytes(), fmt.Errorf("timed out after %s", timeout)
	}
}

func effectiveTimeout(hookCmd Command) time.Duration {
	if hookCmd.Timeout <= 0 {
		return defaultTimeout
	}
	return hookCmd.Timeout
}

// Env returns the environment passed to a hook process.
func Env(ctx Context) []string {
	env := envMap(os.Environ())
	set := func(name, value string) {
		env[name] = value
	}

	set("FANOUT_ROOT", ctx.ProjectRoot)
	set("FANOUT_PARENT", ctx.Parent)
	if ctx.IssueNum > 0 {
		set("FANOUT_ISSUE_NUM", strconv.Itoa(ctx.IssueNum))
	} else {
		set("FANOUT_ISSUE_NUM", "")
	}
	set("FANOUT_TASK_ID", ctx.TaskID)
	set("FANOUT_SLUG", ctx.Slug)
	set("FANOUT_PROMPT", ctx.Prompt)
	set("FANOUT_AGENT", ctx.Agent)
	set("FANOUT_TMUX_PANE_ID", ctx.TmuxPaneID)
	set("FANOUT_WORKTREE_PATH", ctx.WorktreePath)
	set("FANOUT_BRANCH", ctx.Branch)
	set("FANOUT_BASE_BRANCH", ctx.BaseBranch)
	set("FANOUT_TARGET_BRANCH", ctx.TargetBranch)

	set("DMUX_ROOT", ctx.ProjectRoot)
	set("DMUX_PANE_ID", compatPaneID(ctx))
	set("DMUX_SLUG", ctx.Slug)
	set("DMUX_PROMPT", ctx.Prompt)
	set("DMUX_AGENT", ctx.Agent)
	set("DMUX_TMUX_PANE_ID", ctx.TmuxPaneID)
	set("DMUX_WORKTREE_PATH", ctx.WorktreePath)
	set("DMUX_BRANCH", ctx.Branch)
	set("DMUX_TARGET_BRANCH", ctx.TargetBranch)

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func hookCommand(command string, ctx Context) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = ctx.ProjectRoot
	cmd.Env = Env(ctx)
	return cmd
}

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	// The process may have already exited between timeout and cleanup.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func compatPaneID(ctx Context) string {
	switch {
	case strings.TrimSpace(ctx.TaskID) != "":
		return "fanout-" + strings.TrimSpace(ctx.Parent) + "-" + strings.TrimSpace(ctx.TaskID)
	case ctx.IssueNum > 0:
		return "fanout-" + strings.TrimSpace(ctx.Parent) + "-" + strconv.Itoa(ctx.IssueNum)
	default:
		return ""
	}
}

func parseType(raw string) (Type, bool) {
	hook := Type(raw)
	switch hook {
	case BeforePaneCreate, WorktreeCreated, BeforePaneClose, PaneClosed, BeforeWorktreeRemove, WorktreeRemoved, PreMerge, PostMerge:
		return hook, true
	default:
		return "", false
	}
}

func warnf(lg Logger, format string, a ...any) {
	if lg != nil {
		lg.Warn(format, a...)
	}
}
