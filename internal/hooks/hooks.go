// Package hooks runs fanout lifecycle hooks from repository and user config.
package hooks

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Type names a lifecycle hook executable.
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

// Result describes one blocking hook invocation.
type Result struct {
	Ran    bool
	Path   string
	Output []byte
	Err    error
}

// OK reports whether the hook was skipped or ran successfully.
func (r Result) OK() bool {
	return r.Err == nil
}

// CandidatePaths returns hook paths in priority order.
func CandidatePaths(projectRoot string, hook Type) []string {
	return []string{
		filepath.Join(projectRoot, ".fanout-hooks", string(hook)),
		filepath.Join(projectRoot, ".fanout", "hooks", string(hook)),
		filepath.Join(UserHooksDir(), string(hook)),
	}
}

// UserHooksDir returns the user hook directory.
func UserHooksDir() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "fanout", "hooks")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".config", "fanout", "hooks")
	}
	return filepath.Join("fanout", "hooks")
}

// Find returns the first executable hook path, warning about unusable matches.
func Find(projectRoot string, hook Type, lg Logger) (string, bool) {
	return find(projectRoot, hook, lg, true)
}

// FindQuiet returns the first executable hook path without warning about
// unusable matches.
func FindQuiet(projectRoot string, hook Type) (string, bool) {
	return find(projectRoot, hook, nil, false)
}

func find(projectRoot string, hook Type, lg Logger, warn bool) (string, bool) {
	for _, path := range CandidatePaths(projectRoot, hook) {
		st, err := os.Stat(path)
		if err != nil {
			if warn && !os.IsNotExist(err) {
				warnf(lg, "%s hook %s: stat failed: %v", hook, path, err)
			}
			continue
		}
		if !st.Mode().IsRegular() {
			if warn {
				warnf(lg, "%s hook %s: not a regular file; ignored", hook, path)
			}
			continue
		}
		if st.Mode().Perm()&0o111 == 0 {
			if warn {
				warnf(lg, "%s hook %s: not executable; ignored", hook, path)
			}
			continue
		}
		return path, true
	}
	return "", false
}

// RunBackground starts hook asynchronously. Hook failures after process start
// are intentionally not observed.
func RunBackground(hook Type, ctx Context, enabled bool, lg Logger) Result {
	if !enabled {
		return Result{}
	}
	path, ok := Find(ctx.ProjectRoot, hook, lg)
	if !ok {
		return Result{}
	}
	cmd := hookCommand(path, ctx)
	if err := cmd.Start(); err != nil {
		warnf(lg, "%s hook %s: start failed: %v", hook, path, err)
		return Result{Ran: true, Path: path, Err: err}
	}
	go func() {
		_ = cmd.Wait()
	}()
	return Result{Ran: true, Path: path}
}

// RunBlocking runs hook synchronously and returns captured output.
func RunBlocking(hook Type, ctx Context, enabled bool, lg Logger) Result {
	if !enabled {
		return Result{}
	}
	path, ok := Find(ctx.ProjectRoot, hook, lg)
	if !ok {
		return Result{}
	}
	cmd := hookCommand(path, ctx)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Result{
			Ran:    true,
			Path:   path,
			Output: out,
			Err:    fmt.Errorf("%s hook %s failed: %w", hook, path, err),
		}
	}
	return Result{Ran: true, Path: path, Output: out}
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

func hookCommand(path string, ctx Context) *exec.Cmd {
	cmd := exec.Command(path)
	cmd.Dir = ctx.ProjectRoot
	cmd.Env = Env(ctx)
	return cmd
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

func warnf(lg Logger, format string, a ...any) {
	if lg != nil {
		lg.Warn(format, a...)
	}
}
