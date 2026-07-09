// Package panelaunch owns the pane-creation orchestration shared by the
// issue/plan batch lanes, the TUI watch and manual launchers, and the
// attached-agent / shell pane paths: briefing write, worktree preparation,
// tmux split and decoration, state recording, metadata write, and failure
// cleanup.
package panelaunch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelayout"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/displayname"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	fanoutruntime "github.com/butaosuinu/fanout/internal/infra/runtime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const (
	// ManualParentRef is the reserved parent ref recorded for manual panes.
	ManualParentRef = "@manual"
	// WatchParentRef is the reserved parent ref recorded for watcher-launched
	// standalone issue panes.
	WatchParentRef = "@watch"
	// CodexPlanTUIStartupTimeout bounds the wait for a Codex Plan Mode TUI to
	// report readiness after its pane was created.
	CodexPlanTUIStartupTimeout = 30 * time.Second
)

// BaseRefreshSkippedNotice prefixes the warning logged when a best-effort base
// refresh is tolerated. launchManualPaneFromTUI (cmd/fanout) scrapes it from
// buffered launch output so the TUI can surface the skip in its success notice.
const BaseRefreshSkippedNotice = "base branch refresh skipped"

// Request describes one pane to create.
type Request struct {
	ParentRef           string
	Number              int
	TaskID              string
	Title               string
	Body                string
	Wave                string
	BriefingPath        string
	BriefingBody        string
	ShortTitle          string
	Slug                string
	DisplayNameOverride string
	BranchName          string
	Prompt              string
	SourceParent        string
	SourceIssueNum      int
	SourceTaskID        string
	Agent               string
	AgentCommand        string
	CodexPlanMode       bool
	CodexPlanStatusPath string
	// ShellKey is a @fanout_shell_key liveness token for panes recorded with
	// the repo root as their worktree path (the plan fan-out coordinator):
	// the root contains every fanout pane, so the path-containment liveness
	// check cannot protect such rows against tmux pane id reuse.
	ShellKey string
	Hooks    hooks.Config
	Worktree worktree.Plan
}

// created is the result of a successful launch.
type created struct {
	Request  Request
	PaneID   string
	Prepared worktree.Result
}

// ManualOptions parameterizes NewManualRequest.
type ManualOptions struct {
	Title  string
	Body   string
	Agent  string
	Prompt string
}

// StateRecorder is the slice of the locked state store a launch needs:
// recording the new pane and rolling it back when a later step fails.
type StateRecorder interface {
	RecordPane(state.Pane) error
	RemovePane(parent string, issueNum int) error
	RemoveTaskPane(parent, taskID string) error
}

// Launcher bundles the per-run dependencies every pane launch needs.
type Launcher struct {
	Cfg         *cliflags.Config
	Log         *log.Logger
	Info        *fanoutruntime.Info
	Recorder    StateRecorder
	Palette     log.Palette
	CommandName string
}

// LaunchOK is launch without the created-pane detail.
func (l *Launcher) LaunchOK(req Request) bool {
	_, ok := l.launch(req)
	return ok
}

// launch creates one worktree-backed agent pane: briefing write, worktree
// preparation, hooks, tmux split and decoration, optional Codex Plan Mode
// wait, state recording, and worktree metadata write. A failure after the
// split tears the partial launch down.
func (l *Launcher) launch(req Request) (created, bool) {
	agentCmd, cmdErr := buildAgentCommand(l.Cfg, req, l.CommandName)
	if cmdErr != nil {
		l.Log.Err("%s: %v", paneLogLabel(req), cmdErr)
		return created{}, false
	}
	req.AgentCommand = agentCmd
	// Strict children (issue/plan) fail fast on a known refresh error. Best-effort
	// manual panes fall through; the skip is surfaced once via Prepare's
	// RefreshWarning below so it is not logged twice.
	if req.Worktree.Refresh && req.Worktree.RefreshError != nil && !req.Worktree.RefreshBestEffort {
		l.Log.Err("%s: prepare worktree: %v", paneLogLabel(req), req.Worktree.RefreshError)
		return created{}, false
	}

	if req.BriefingPath != "" && !l.Cfg.DryRun && !l.writeBriefing(req) {
		return created{}, false
	}

	logPaneRequest(req, l.Log)

	if l.Cfg.DryRun {
		printPaneDryRun(req, l.Info.Target, l.Log, l.Palette)
		return created{}, true
	}

	prepared, prepErr := worktree.Prepare(worktree.Options{
		ProjectRoot:        l.Info.ProjectRoot,
		Slug:               req.Slug,
		BranchName:         req.BranchName,
		BaseBranch:         req.Worktree.BaseBranch,
		NoRefresh:          l.Cfg.NoRefresh,
		AllowMissingOrigin: req.Worktree.AllowMissingOrigin,
		RefreshBestEffort:  req.Worktree.RefreshBestEffort,
	})
	// Surface a tolerated refresh skip before the error check so the diagnostic
	// is not lost when a later worktree step fails.
	if prepared.RefreshWarning != nil {
		l.Log.Warn("%s: %s: %v", paneLogLabel(req), BaseRefreshSkippedNotice, prepared.RefreshWarning)
	}
	if prepErr != nil {
		l.Log.Err("%s: prepare worktree: %v", paneLogLabel(req), prepErr)
		return created{}, false
	}
	if prepared.AlreadyExists {
		l.Log.Err("%s: worktree path already exists during launch: %s (duplicate slug or concurrent fanout run)", paneLogLabel(req), prepared.WorktreePath)
		return created{}, false
	}

	if result := hooks.RunBlocking(hooks.WorktreeCreated, paneHookContext(req, l.Info.ProjectRoot, prepared.WorktreePath, ""), req.Hooks, l.Log); !result.OK() {
		l.Log.Err("%s: %v", paneLogLabel(req), result.Err)
		printPaneHookOutput(result, l.Log)
		failCleanup(paneLogLabel(req), l.Info.Target, "", &prepared, l.Log)
		return created{}, false
	}
	hooks.RunBackground(hooks.BeforePaneCreate, paneHookContext(req, l.Info.ProjectRoot, prepared.WorktreePath, ""), req.Hooks, l.Log)

	paneID, ok := l.splitAndDecorate(req, prepared.WorktreePath, decorateOpts{})
	if !ok {
		failCleanup(paneLogLabel(req), l.Info.Target, "", &prepared, l.Log)
		return created{}, false
	}
	var codexPlanStatus codexapp.Status
	if req.CodexPlanMode {
		var planErr error
		codexPlanStatus, planErr = codexapp.WaitReady(req.CodexPlanStatusPath, CodexPlanTUIStartupTimeout)
		if planErr != nil {
			l.Log.Err("%s: start Codex Plan Mode TUI in pane %s: %v", paneLogLabel(req), paneID, planErr)
			failCleanup(paneLogLabel(req), l.Info.Target, paneID, &prepared, l.Log)
			return created{}, false
		}
		_ = os.Remove(req.CodexPlanStatusPath)
	}
	if l.Recorder != nil {
		entry := statePane(req, paneID, prepared.WorktreePath, time.Now().UTC(), codexPlanStatus)
		if err := l.Recorder.RecordPane(entry); err != nil {
			l.Log.Err("%s: write fanout state: %v", paneLogLabel(req), err)
			failCleanup(paneLogLabel(req), l.Info.Target, paneID, &prepared, l.Log)
			return created{}, false
		}
	}
	if err := displayname.WriteFanoutMetadata(prepared.WorktreePath, displayname.FanoutMetadata{
		Agent:          req.Agent,
		DisplayName:    paneTitle(req),
		BranchName:     req.BranchName,
		Slug:           req.Slug,
		WorktreePath:   prepared.WorktreePath,
		CodexThreadID:  codexPlanStatus.ThreadID,
		CodexSessionID: codexPlanStatus.SessionID,
	}); err != nil {
		l.Log.Err("%s: write worktree metadata: %v", paneLogLabel(req), err)
		rollbackState(l.Recorder, req, l.Log)
		failCleanup(paneLogLabel(req), l.Info.Target, paneID, &prepared, l.Log)
		return created{}, false
	}
	l.Log.Ok("%s: pane %s created in %s", paneLogLabel(req), paneID, prepared.WorktreePath)
	return created{Request: req, PaneID: paneID, Prepared: prepared}, true
}

// writeBriefing creates the briefing directory and writes req.BriefingBody to
// req.BriefingPath, logging under the pane's label on failure.
func (l *Launcher) writeBriefing(req Request) bool {
	if err := os.MkdirAll(filepath.Dir(req.BriefingPath), 0o755); err != nil {
		l.Log.Err("%s: write briefing: %v", paneLogLabel(req), err)
		return false
	}
	if err := os.WriteFile(req.BriefingPath, []byte(req.BriefingBody), 0o644); err != nil {
		l.Log.Err("%s: write briefing: %v", paneLogLabel(req), err)
		return false
	}
	return true
}

// Attach creates one agent pane attached to an existing directory (no
// worktree preparation, no WorktreeCreated hook, no metadata write).
func (l *Launcher) Attach(req Request, targetPath string) bool {
	agentCmd, err := buildAgentCommand(l.Cfg, req, l.CommandName)
	if err != nil {
		l.Log.Err("%s: %v", paneLogLabel(req), err)
		return false
	}
	req.AgentCommand = agentCmd
	if req.BriefingPath != "" && !l.writeBriefing(req) {
		return false
	}

	l.Log.Info("%s: attach %s to %s", paneLogLabel(req), req.Agent, targetPath)
	l.Log.Dim("  slug -> %s", req.Slug)
	l.Log.Dim("  worktree -> %s", targetPath)
	if req.CodexPlanMode {
		l.Log.Dim("  codex-plan-mode -> app-server + interactive Codex TUI /plan session")
	}
	hooks.RunBackground(hooks.BeforePaneCreate, paneHookContext(req, l.Info.ProjectRoot, targetPath, ""), req.Hooks, l.Log)

	paneID, ok := l.splitAndDecorate(req, targetPath, decorateOpts{strictShellKey: true})
	if !ok {
		return false
	}
	codexPlanStatus := codexapp.Status{}
	if req.CodexPlanMode {
		var planErr error
		codexPlanStatus, planErr = codexapp.WaitReady(req.CodexPlanStatusPath, CodexPlanTUIStartupTimeout)
		if planErr != nil {
			l.Log.Err("%s: start Codex Plan Mode TUI in pane %s: %v", paneLogLabel(req), paneID, planErr)
			failCleanup("", l.Info.Target, paneID, nil, nil)
			return false
		}
		_ = os.Remove(req.CodexPlanStatusPath)
	}
	if l.Recorder != nil {
		entry := statePane(req, paneID, targetPath, time.Now().UTC(), codexPlanStatus)
		entry.Kind = state.PaneKindAttachedAgent
		if err := l.Recorder.RecordPane(entry); err != nil {
			l.Log.Err("%s: write fanout state: %v", paneLogLabel(req), err)
			failCleanup("", l.Info.Target, paneID, nil, nil)
			return false
		}
	}
	l.Log.Ok("%s: pane %s attached to %s", paneLogLabel(req), paneID, targetPath)
	return true
}

// decorateOpts selects the strictness of splitAndDecorate's post-split steps.
type decorateOpts struct {
	// strictShellKey fails the launch when a non-empty req.ShellKey cannot be
	// stamped on the pane. Only the attached-agent path records keyed rows
	// through splitAndDecorate today.
	strictShellKey bool
}

// splitAndDecorate runs the tmux steps shared by launch and Attach: split the
// window with the agent command, stamp the pane title/label/border/hints
// (best-effort with a warning each), and re-layout the window. It logs the
// error and returns ok=false when the split fails (the caller owns any
// worktree cleanup); a failed strict shell key tears the pane down itself.
func (l *Launcher) splitAndDecorate(req Request, workPath string, opts decorateOpts) (string, bool) {
	paneID, splitErr := tmuxrun.SplitPaneWithAgentCommand(l.Info.Target, workPath, req.AgentCommand)
	if splitErr != nil {
		l.Log.Err("%s: %v", paneLogLabel(req), splitErr)
		return "", false
	}
	if opts.strictShellKey && req.ShellKey != "" {
		// The liveness key is not best-effort: a recorded key that never reaches
		// the tmux pane would leave the row permanently stale.
		if err := tmuxrun.SetPaneShellKey(paneID, req.ShellKey); err != nil {
			l.Log.Err("%s: set pane liveness key: %v", paneLogLabel(req), err)
			failCleanup("", l.Info.Target, paneID, nil, nil)
			return "", false
		}
	}
	if err := tmuxrun.SetPaneTitle(paneID, paneTitle(req)); err != nil {
		l.Log.Warn("%s: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneLabel(paneID, paneBorderLabel(req)); err != nil {
		l.Log.Warn("%s: pane border label: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.EnablePaneBorderTitles(paneID); err != nil {
		l.Log.Warn("%s: pane border titles: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneProjectRoot(paneID, l.Info.ProjectRoot); err != nil {
		l.Log.Warn("%s: dashboard project root hint: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneWorktreePath(paneID, workPath); err != nil {
		l.Log.Warn("%s: worktree path hint: %v", paneLogLabel(req), err)
	}
	// Re-layout right after the split so the new pane is sized into the grid
	// immediately — a Codex Plan Mode pane otherwise sits at the ~half-width split
	// for the whole (up to 30s) startup wait below. A failed launch reconciles any
	// spacer this created via failCleanup's relayout, so no orphan remains.
	if err := panelayout.Apply(l.Info.Target, panelayout.Create); err != nil {
		l.Log.Warn("%s: %v", paneLogLabel(req), err)
	}
	return paneID, true
}

func statePane(req Request, paneID, worktreePath string, now time.Time, codexPlanStatus codexapp.Status) state.Pane {
	return state.Pane{
		Parent:         req.ParentRef,
		IssueNum:       req.Number,
		TaskID:         req.TaskID,
		Slug:           req.Slug,
		BranchName:     req.BranchName,
		BaseBranch:     req.Worktree.BaseBranch,
		PaneID:         paneID,
		SourceParent:   req.SourceParent,
		SourceIssueNum: req.SourceIssueNum,
		SourceTaskID:   req.SourceTaskID,
		Agent:          req.Agent,
		ShellKey:       req.ShellKey,
		CodexPlanMode:  req.CodexPlanMode,
		CodexThreadID:  codexPlanStatus.ThreadID,
		CodexSessionID: codexPlanStatus.SessionID,
		DisplayName:    paneTitle(req),
		WorktreePath:   worktreePath,
		Prompt:         req.Prompt,
		Wave:           req.Wave,
		CreatedAt:      now.Format(time.RFC3339),
		AgentStatus:    "running",
	}
}

func buildAgentCommand(cfg *cliflags.Config, req Request, commandName string) (string, error) {
	if req.CodexPlanMode {
		if req.Agent != "codex" {
			return "", fmt.Errorf("--codex-plan-mode requires --agent codex")
		}
		if cfg.DryRun {
			return codexapp.LaunchCommand(commandName, "codex", req.Prompt, req.CodexPlanStatusPath), nil
		}
		codexPath, err := agent.ResolveExecutable("codex")
		if err != nil {
			return "", err
		}
		fanoutPath, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve fanout executable: %w", err)
		}
		return "PATH=" + agent.ShellQuote(os.Getenv("PATH")) + " " + codexapp.LaunchCommand(fanoutPath, codexPath, req.Prompt, req.CodexPlanStatusPath), nil
	}
	if cfg.DryRun {
		return agent.BuildCommand(req.Agent, req.Prompt)
	}
	return agent.BuildResolvedCommand(req.Agent, req.Prompt)
}

func logPaneRequest(req Request, lg *log.Logger) {
	lg.Info("%s: %s", paneLogLabel(req), req.ShortTitle)
	if req.BriefingPath != "" {
		lg.Dim("  briefing -> %s", req.BriefingPath)
	}
	lg.Dim("  slug -> %s", req.Slug)
	lg.Dim("  worktree -> %s", req.Worktree.WorktreePath)
	lg.Dim("  branch -> %s", req.BranchName)
	lg.Dim("  base -> %s", req.Worktree.BaseBranch)
	if req.Worktree.RefreshSkippedReason != "" {
		lg.Dim("  refresh -> skipped (%s)", req.Worktree.RefreshSkippedReason)
	}
	if req.DisplayNameOverride != "" {
		lg.Dim("  display-name -> %s", req.DisplayNameOverride)
	}
	if req.CodexPlanMode {
		lg.Dim("  codex-plan-mode -> app-server + interactive Codex TUI /plan session")
	}
}

func printPaneDryRun(req Request, target string, lg *log.Logger, c log.Palette) {
	if req.BriefingPath != "" || req.BriefingBody != "" {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	}
	if req.CodexPlanMode {
		fmt.Fprintf(lg.Stdout(), "  %scodex plan mode%s: app-server + interactive Codex TUI /plan session\n", c.Dim, c.Reset)
	}
	if req.Worktree.Refresh {
		details := req.Worktree.RefreshDetails
		fmt.Fprintf(lg.Stdout(), "    %s$ git -C %s fetch --quiet --no-tags origin %s%s\n", c.Dim, shellQuote(req.Worktree.ProjectRoot), shellQuote(details.FetchBranch), c.Reset)
		if details.LocalBranch != "" {
			fmt.Fprintf(lg.Stdout(), "    %s# may fast-forward the local base before worktree creation%s\n", c.Dim, c.Reset)
			fmt.Fprintf(lg.Stdout(), "    %s$ git -C %s branch -f %s %s%s\n", c.Dim, shellQuote(req.Worktree.ProjectRoot), shellQuote(details.LocalBranch), shellQuote(details.OriginRef), c.Reset)
			fmt.Fprintf(lg.Stdout(), "    %s# if the base is checked out elsewhere, fanout uses merge --ff-only in that worktree%s\n", c.Dim, c.Reset)
		}
	} else if req.Worktree.RefreshSkippedReason != "" {
		fmt.Fprintf(lg.Stdout(), "    %s# skip base refresh: %s%s\n", c.Dim, req.Worktree.RefreshSkippedReason, c.Reset)
	}
	fmt.Fprintf(lg.Stdout(), "    %s$ git -C %s worktree add -b %s %s %s%s\n",
		c.Dim,
		shellQuote(req.Worktree.ProjectRoot),
		shellQuote(req.Worktree.BranchName),
		shellQuote(req.Worktree.WorktreePath),
		shellQuote(req.Worktree.BaseBranch),
		c.Reset)
	if target != "" {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux split-window -t %s -d -h -P -F '#{pane_id}' -c %s %s%s\n", c.Dim, shellQuote(target), shellQuote(req.Worktree.WorktreePath), shellQuote(tmuxrun.BuildPaneLaunchCommand(req.AgentCommand)), c.Reset)
	} else {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux split-window -d -h -P -F '#{pane_id}' -c %s %s%s\n", c.Dim, shellQuote(req.Worktree.WorktreePath), shellQuote(tmuxrun.BuildPaneLaunchCommand(req.AgentCommand)), c.Reset)
	}
	if title := paneTitle(req); title != "" {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-pane -t <pane_id> -T %s%s\n", c.Dim, shellQuote(title), c.Reset)
	}
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux set-option -p -t <pane_id> @fanout_pane_label %s%s\n", c.Dim, shellQuote(tmuxrun.NeutralizePaneLabel(paneBorderLabel(req))), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux set-option -w -t <pane_id> pane-border-status top%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux set-option -w -t <pane_id> pane-border-format %s%s\n", c.Dim, shellQuote(tmuxrun.PaneBorderFormat()), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux set-option -w -t <pane_id> pane-active-border-style %s%s\n", c.Dim, shellQuote(tmuxrun.PaneActiveBorderStyle()), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux set-option -w -t <pane_id> pane-border-style %s%s\n", c.Dim, shellQuote(tmuxrun.PaneBorderStyle()), c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# would re-layout the window: fanout grid (sidebar + comfortable-width grid),%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s#   falling back to main-vertical then tiled%s\n", c.Dim, c.Reset)
	if req.CodexPlanMode {
		fmt.Fprintf(lg.Stdout(), "    %s# fanout waits for Codex TUI /plan thread startup before recording state%s\n", c.Dim, c.Reset)
		fmt.Fprintf(lg.Stdout(), "    %s# status file: %s%s\n", c.Dim, shellQuote(req.CodexPlanStatusPath), c.Reset)
	}
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/state.json with paneId <pane_id>%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/worktree-metadata.json in the child worktree%s\n", c.Dim, c.Reset)
	printPaneHookDryRun(req, lg, c)
	lg.Ok("%s: dry-run complete", paneLogLabel(req))
}

func printPaneHookDryRun(req Request, lg *log.Logger, c log.Palette) {
	for _, hook := range []hooks.Type{hooks.WorktreeCreated, hooks.BeforePaneCreate} {
		for _, hookCmd := range req.Hooks.Events[hook] {
			fmt.Fprintf(lg.Stdout(), "    %s# hook %s: /bin/sh -c %s%s\n", c.Dim, hook, shellQuote(hookCmd.Command), c.Reset)
		}
	}
}

func paneHookContext(req Request, projectRoot, worktreePath, paneID string) hooks.Context {
	if worktreePath == "" {
		worktreePath = req.Worktree.WorktreePath
	}
	return hooks.Context{
		ProjectRoot:  projectRoot,
		Parent:       req.ParentRef,
		IssueNum:     req.Number,
		TaskID:       req.TaskID,
		Slug:         req.Slug,
		Prompt:       req.Prompt,
		Agent:        req.Agent,
		TmuxPaneID:   paneID,
		WorktreePath: worktreePath,
		Branch:       req.BranchName,
		BaseBranch:   req.Worktree.BaseBranch,
		TargetBranch: req.Worktree.BaseBranch,
	}
}

func printPaneHookOutput(result hooks.Result, lg *log.Logger) {
	if s := strings.TrimSpace(string(result.Output)); s != "" {
		fmt.Fprintln(lg.Stderr(), s)
	}
}

func paneLogLabel(req Request) string {
	if req.TaskID != "" {
		return req.TaskID
	}
	return fmt.Sprintf("#%d", req.Number)
}

func rollbackState(recorder StateRecorder, req Request, lg *log.Logger) {
	if recorder == nil {
		return
	}
	var err error
	if req.TaskID != "" {
		err = recorder.RemoveTaskPane(req.ParentRef, req.TaskID)
	} else {
		err = recorder.RemovePane(req.ParentRef, req.Number)
	}
	if err != nil {
		lg.Warn("%s: could not roll back fanout state: %v", paneLogLabel(req), err)
	}
}

// failCleanup tears down a partially created launch: it kills the pane (when
// one exists), reconciles the window layout, and removes a created worktree
// (when prepared is non-nil). A nil lg makes every step silent best-effort
// (the attached-agent path); otherwise each failed step logs a warning.
func failCleanup(label, relayoutTarget, paneID string, prepared *worktree.Result, lg *log.Logger) {
	if paneID != "" {
		if err := tmuxrun.KillPane(paneID); err != nil && lg != nil {
			lg.Warn("%s: cleanup incomplete pane %s: %v", label, paneID, err)
		}
		// The failed pane is gone; re-tile so neither it nor a spacer that an
		// early/concurrent relayout may have created is left dangling in the grid.
		if err := panelayout.Apply(relayoutTarget, panelayout.Close); err != nil && lg != nil {
			lg.Warn("%s: relayout after failed launch: %v", label, err)
		}
	}
	if prepared == nil {
		return
	}
	if err := worktree.CleanupCreated(*prepared); err != nil && lg != nil {
		lg.Warn("%s: cleanup incomplete worktree %s: %v", label, prepared.WorktreePath, err)
	}
}

// shellQuote mirrors cmd/fanout's dry-run quoting (report.go): the byte-exact
// output of printPaneDryRun is pinned by the Tier 2 goldens.
func shellQuote(s string) string {
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
