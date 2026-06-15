package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/displayname"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	"github.com/butaosuinu/fanout/internal/planspec"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

const (
	manualPaneParentRef        = "@manual"
	codexPlanTUIStartupTimeout = 30 * time.Second
	codexPlanTUIStartupPoll    = 200 * time.Millisecond
)

var errCodexPlanStartupTimeout = errors.New("timed out waiting for Codex Plan TUI startup")

type paneRequest struct {
	ParentRef           string
	Number              int
	TaskID              string
	Title               string
	Body                string
	Wave                string
	BriefingPath        string
	BriefingBody        string
	WriteBriefingDryRun bool
	ShortTitle          string
	Slug                string
	DisplayNameOverride string
	BranchName          string
	Prompt              string
	Agent               string
	AgentCommand        string
	CodexPlanMode       bool
	CodexPlanStatusPath string
	Worktree            worktree.Plan
}

type manualPaneOptions struct {
	Title      string
	Body       string
	Slug       string
	BranchName string
	Agent      string
	Prompt     string
}

type paneStateRecorder interface {
	RecordPane(state.Pane) error
	RemovePane(parent string, issueNum int) error
	RemoveTaskPane(parent, taskID string) error
}

func createPaneForIssue(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, issue ghissue.Issue, resolvedSettings settings.Settings, recorder paneStateRecorder, sharedAcrossParents bool, c log.Palette, commandName string, teamCtx *briefing.TeamContext) bool {
	req := newPaneRequest(cfg, info.ProjectRoot, issue, resolvedSettings, sharedAcrossParents, teamCtx)
	return createPane(cfg, lg, info, req, recorder, c, commandName)
}

func createPane(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, req paneRequest, recorder paneStateRecorder, c log.Palette, commandName string) bool {
	agentCmd, err := buildAgentCommand(cfg, req, commandName)
	if err != nil {
		lg.Err("%s: %v", paneLogLabel(req), err)
		return false
	}
	req.AgentCommand = agentCmd
	if req.Worktree.Refresh && req.Worktree.RefreshError != nil {
		lg.Err("%s: prepare worktree: %v", paneLogLabel(req), req.Worktree.RefreshError)
		return false
	}

	if req.BriefingPath != "" && (!cfg.DryRun || req.WriteBriefingDryRun) {
		if err = os.WriteFile(req.BriefingPath, []byte(req.BriefingBody), 0o644); err != nil {
			lg.Err("%s: write briefing: %v", paneLogLabel(req), err)
			return false
		}
	}

	logPaneRequest(req, lg)

	if cfg.DryRun {
		printPaneDryRun(req, info.Target, lg, c)
		return true
	}

	prepared, err := worktree.Prepare(worktree.Options{
		ProjectRoot: info.ProjectRoot,
		Slug:        req.Slug,
		BranchName:  req.BranchName,
		BaseBranch:  req.Worktree.BaseBranch,
		NoRefresh:   cfg.NoRefresh,
	})
	if err != nil {
		lg.Err("%s: prepare worktree: %v", paneLogLabel(req), err)
		return false
	}
	if prepared.AlreadyExists {
		lg.Err("%s: worktree path already exists during launch: %s (duplicate slug or concurrent fanout run)", paneLogLabel(req), prepared.WorktreePath)
		return false
	}

	paneID, err := tmuxrun.SplitPaneWithAgentCommand(info.Target, prepared.WorktreePath, req.AgentCommand)
	if err != nil {
		lg.Err("%s: %v", paneLogLabel(req), err)
		cleanupFailedLaunch(paneLogLabel(req), "", prepared, lg)
		return false
	}
	if err := tmuxrun.SetPaneTitle(paneID, paneTitle(req)); err != nil {
		lg.Warn("%s: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SetPaneProjectRoot(paneID, info.ProjectRoot); err != nil {
		lg.Warn("%s: dashboard project root hint: %v", paneLogLabel(req), err)
	}
	if err := tmuxrun.SelectTiled(info.Target); err != nil {
		lg.Warn("%s: %v", paneLogLabel(req), err)
	}
	if req.CodexPlanMode {
		if err := waitForCodexPlanTUIReady(req.CodexPlanStatusPath, codexPlanTUIStartupTimeout); err != nil {
			lg.Err("%s: start Codex Plan Mode TUI in pane %s: %v", paneLogLabel(req), paneID, err)
			cleanupFailedLaunch(paneLogLabel(req), paneID, prepared, lg)
			return false
		}
		_ = os.Remove(req.CodexPlanStatusPath)
	}
	if recorder != nil {
		entry := statePane(req, paneID, prepared.WorktreePath, time.Now().UTC())
		if err := recorder.RecordPane(entry); err != nil {
			lg.Err("%s: write fanout state: %v", paneLogLabel(req), err)
			cleanupFailedLaunch(paneLogLabel(req), paneID, prepared, lg)
			return false
		}
	}
	if err := displayname.WriteFanoutMetadata(prepared.WorktreePath, displayname.FanoutMetadata{
		Agent:        req.Agent,
		DisplayName:  paneTitle(req),
		BranchName:   req.BranchName,
		Slug:         req.Slug,
		WorktreePath: prepared.WorktreePath,
	}); err != nil {
		lg.Err("%s: write worktree metadata: %v", paneLogLabel(req), err)
		rollbackState(recorder, req, lg)
		cleanupFailedLaunch(paneLogLabel(req), paneID, prepared, lg)
		return false
	}
	lg.Ok("%s: pane %s created in %s", paneLogLabel(req), paneID, prepared.WorktreePath)
	return true
}

func statePane(req paneRequest, paneID, worktreePath string, now time.Time) state.Pane {
	return state.Pane{
		Parent:        req.ParentRef,
		IssueNum:      req.Number,
		TaskID:        req.TaskID,
		Slug:          req.Slug,
		BranchName:    req.BranchName,
		BaseBranch:    req.Worktree.BaseBranch,
		PaneID:        paneID,
		Agent:         req.Agent,
		CodexPlanMode: req.CodexPlanMode,
		DisplayName:   paneTitle(req),
		WorktreePath:  worktreePath,
		Prompt:        req.Prompt,
		Wave:          req.Wave,
		CreatedAt:     now.Format(time.RFC3339),
		AgentStatus:   "running",
	}
}

func buildAgentCommand(cfg *cliflags.Config, req paneRequest, commandName string) (string, error) {
	if req.CodexPlanMode {
		if req.Agent != "codex" {
			return "", fmt.Errorf("--codex-plan-mode requires --agent codex")
		}
		if cfg.DryRun {
			return buildCodexPlanTUILaunchCommand(commandName, "codex", req.Prompt, req.CodexPlanStatusPath), nil
		}
		codexPath, err := agent.ResolveExecutable("codex")
		if err != nil {
			return "", err
		}
		fanoutPath, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve fanout executable: %w", err)
		}
		return "PATH=" + agent.ShellQuote(os.Getenv("PATH")) + " " + buildCodexPlanTUILaunchCommand(fanoutPath, codexPath, req.Prompt, req.CodexPlanStatusPath), nil
	}
	if cfg.DryRun {
		return agent.BuildCommand(req.Agent, req.Prompt)
	}
	return agent.BuildResolvedCommand(req.Agent, req.Prompt)
}

func buildCodexPlanTUILaunchCommand(fanoutPath, codexPath, prompt, statusPath string) string {
	if strings.TrimSpace(fanoutPath) == "" {
		fanoutPath = "fanout"
	}
	args := []string{
		fanoutPath,
		"__codex-plan-tui",
		"--codex", codexPath,
		"--prompt", prompt,
		"--status-file", statusPath,
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = agent.ShellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func newPaneRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue, resolvedSettings settings.Settings, sharedAcrossParents bool, teamCtx *briefing.TeamContext) paneRequest {
	slug := naming.Slug(issue.Title, issue.Number)
	slugOverridden := false
	branchOverride := ""
	agentName := cfg.EffectiveAgentForIssue(issue.Number)
	req := paneRequest{
		ParentRef:    cfg.ParentRef,
		Number:       issue.Number,
		Title:        issue.Title,
		Body:         issue.Body,
		Wave:         issue.Wave,
		BriefingPath: briefing.Path(projectRoot, issue.Number),
		// Existing issue dry-runs write briefings; Tier 1 tests depend on that.
		WriteBriefingDryRun: true,
		ShortTitle:          shortIssueTitle(issue.Title),
		Slug:                slug,
		Agent:               agentName,
		CodexPlanMode:       cfg.CodexPlanModeEnabled(),
	}
	if name := cfg.FindName(issue.Number); name != nil {
		if name.SlugHint != "" {
			req.Slug = naming.EnsureIssueSuffix(name.SlugHint, issue.Number)
			slugOverridden = true
		}
		req.DisplayNameOverride = name.DisplayName
		branchOverride = name.BranchName
	}
	if sharedAcrossParents && !slugOverridden {
		req.Slug = naming.QualifySlugForParent(req.Slug, cfg.ParentRef, issue.Number)
	}
	req.BranchName = naming.BranchName(branchOverride, cfg.BranchPrefix, req.Slug)
	req.Worktree = worktree.BuildPlan(worktree.Options{
		ProjectRoot: projectRoot,
		Slug:        req.Slug,
		BranchName:  req.BranchName,
		BaseBranch:  cfg.BaseBranch,
		NoRefresh:   cfg.NoRefresh,
	})
	req.BriefingBody = briefing.Render(issue.Number, issue.Title, issue.Body, agentName, req.Worktree.BaseBranch, resolvedSettings, req.CodexPlanMode, teamCtx)
	req.Prompt = oneLinePrompt(req.ParentRef, req)
	if req.CodexPlanMode {
		req.CodexPlanStatusPath = codexPlanStatusPath(projectRoot, issue.Number, cfg.DryRun)
	}
	return req
}

func newTaskPaneRequest(cfg *cliflags.Config, projectRoot string, spec planspec.Spec, task planspec.Task, resolvedSettings settings.Settings, teamCtx *briefing.TeamContext) paneRequest {
	slug := planTaskSlug(spec.Plan.Slug, task)
	branchName := task.Branch
	if branchName == "" {
		branchName = naming.BranchName("", cfg.BranchPrefix, slug)
	}
	agentName := cfg.EffectiveAgent(task.ID)
	req := paneRequest{
		ParentRef:           planParentRef(spec.Plan.Slug),
		Number:              0,
		TaskID:              task.ID,
		Title:               task.Title,
		Body:                task.Briefing,
		Wave:                task.Wave,
		BriefingPath:        briefing.TaskPath(projectRoot, spec.Plan.Slug, task.ID),
		ShortTitle:          shortIssueTitle(task.Title),
		Slug:                slug,
		DisplayNameOverride: task.DisplayName,
		BranchName:          branchName,
		Agent:               agentName,
		Worktree: worktree.BuildPlan(worktree.Options{
			ProjectRoot: projectRoot,
			Slug:        slug,
			BranchName:  branchName,
			BaseBranch:  cfg.BaseBranch,
			NoRefresh:   cfg.NoRefresh,
		}),
	}
	req.BriefingBody = briefing.RenderTask(spec.Plan.Slug, spec.Plan.Title, task.ID, task.Title, task.Briefing, agentName, req.Worktree.BaseBranch, resolvedSettings, teamCtx)
	req.Prompt = taskOneLinePrompt(spec.Plan.Slug, req)
	return req
}

func newManualPaneRequest(cfg *cliflags.Config, projectRoot string, store state.Store, opts manualPaneOptions) paneRequest {
	number := nextSyntheticPaneNumber(store, manualPaneParentRef)
	title := opts.Title
	if title == "" {
		title = "Manual agent"
	}
	slug := opts.Slug
	if slug == "" {
		slug = manualPaneSlug(title, number)
	}
	agentName := opts.Agent
	if agentName == "" {
		agentName = cfg.Agent
	}
	prompt := opts.Prompt
	if prompt == "" {
		prompt = title
	}
	briefingPath := ""
	briefingBody := ""
	if opts.Body != "" {
		briefingPath = briefing.Path(projectRoot, number)
		briefingBody = opts.Body
		prompt = manualPromptWithBriefing(prompt, briefingPath)
	}
	branchName := naming.BranchName(opts.BranchName, cfg.BranchPrefix, slug)
	return paneRequest{
		ParentRef:    manualPaneParentRef,
		Number:       number,
		Title:        title,
		Body:         opts.Body,
		ShortTitle:   shortIssueTitle(title),
		Slug:         slug,
		BranchName:   branchName,
		Prompt:       prompt,
		Agent:        agentName,
		BriefingPath: briefingPath,
		BriefingBody: briefingBody,
		Worktree:     worktree.BuildPlan(worktree.Options{ProjectRoot: projectRoot, Slug: slug, BranchName: branchName, BaseBranch: cfg.BaseBranch, NoRefresh: cfg.NoRefresh}),
	}
}

func manualPromptWithBriefing(prompt, briefingPath string) string {
	prompt = strings.TrimSpace(prompt)
	if strings.Contains(prompt, briefingPath) {
		return prompt
	}
	prompt = strings.TrimRight(prompt, ".")
	if prompt == "" {
		return fmt.Sprintf("read %s and begin.", briefingPath)
	}
	return fmt.Sprintf("%s. read %s for additional context and begin.", prompt, briefingPath)
}

func nextSyntheticPaneNumber(store state.Store, parentRef string) int {
	next := -1
	for _, pane := range store.PanesForParent(parentRef) {
		if pane.IssueNum <= next {
			next = pane.IssueNum - 1
		}
	}
	return next
}

func manualPaneSlug(title string, number int) string {
	base := naming.Slugify(title)
	if base == "" {
		base = "agent"
	}
	if number < 0 {
		number = -number
	}
	prefix := fmt.Sprintf("manual-%d-", number)
	suffix := "-pane"
	maxBase := max(naming.MaxSlugLength-len(prefix)-len(suffix), 1)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "agent"
		}
	}
	return prefix + base + suffix
}

func codexPlanStatusPath(projectRoot string, issueNum int, dryRun bool) string {
	repo := safeCodexPlanTempPart(filepath.Base(projectRoot))
	base := fmt.Sprintf("fanout-codex-plan-%s-%d", repo, issueNum)
	if dryRun {
		return filepath.Join("/tmp", base+".json")
	}
	unique := fmt.Sprintf("%s-%d-%d", base, os.Getpid(), time.Now().UnixNano())
	return filepath.Join("/tmp", unique+".json")
}

func safeCodexPlanTempPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() == 0 {
		return "repo"
	}
	return b.String()
}

func shortIssueTitle(title string) string {
	const maxRunes = 60
	count := 0
	for i := range title {
		if count == maxRunes {
			return title[:i]
		}
		count++
	}
	return title
}

func oneLinePrompt(parentRef string, req paneRequest) string {
	action := "begin"
	if req.CodexPlanMode {
		action = "propose a plan"
	}
	return fmt.Sprintf("%s%d of #%s] %s: %s. read %s and %s.", fanoutTagPrefix, req.Number, parentRef, req.Slug, req.ShortTitle, req.BriefingPath, action)
}

func taskOneLinePrompt(planSlug string, req paneRequest) string {
	return fmt.Sprintf("[fanout %s of plan:%s] %s: %s. read %s and begin.", req.TaskID, planSlug, req.Slug, oneLineText(req.ShortTitle), req.BriefingPath)
}

func oneLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func waitForCodexPlanTUIReady(statusPath string, timeout time.Duration) error {
	if strings.TrimSpace(statusPath) == "" {
		return fmt.Errorf("missing Codex Plan TUI status path")
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		status, err := readCodexPlanTUIStatus(statusPath)
		if err == nil {
			switch status.Status {
			case codexPlanTUIStatusReady:
				return nil
			case codexPlanTUIStatusFailed:
				if status.Error == "" {
					return fmt.Errorf("Codex Plan TUI setup failed") //nolint:staticcheck // ST1005: "Codex Plan TUI" is a proper noun
				}
				return errors.New(status.Error)
			default:
				lastErr = fmt.Errorf("unexpected Codex Plan TUI status %q", status.Status)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("%w after %s; last status error: %w", errCodexPlanStartupTimeout, timeout, lastErr)
			}
			return fmt.Errorf("%w after %s; no status file at %s", errCodexPlanStartupTimeout, timeout, statusPath)
		}
		time.Sleep(codexPlanTUIStartupPoll)
	}
}

func readCodexPlanTUIStatus(path string) (codexPlanTUIStatus, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return codexPlanTUIStatus{}, err
	}
	var status codexPlanTUIStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return codexPlanTUIStatus{}, fmt.Errorf("parse Codex Plan TUI status: %w", err)
	}
	return status, nil
}

func logPaneRequest(req paneRequest, lg *log.Logger) {
	lg.Info("%s: %s", paneLogLabel(req), req.ShortTitle)
	if req.BriefingPath != "" {
		lg.Dim("  briefing -> %s", req.BriefingPath)
	}
	lg.Dim("  slug -> %s", req.Slug)
	lg.Dim("  worktree -> %s", req.Worktree.WorktreePath)
	lg.Dim("  branch -> %s", req.BranchName)
	lg.Dim("  base -> %s", req.Worktree.BaseBranch)
	if req.DisplayNameOverride != "" {
		lg.Dim("  display-name -> %s", req.DisplayNameOverride)
	}
	if req.CodexPlanMode {
		lg.Dim("  codex-plan-mode -> app-server Plan turn + interactive Codex TUI")
	}
}

func printPaneDryRun(req paneRequest, target string, lg *log.Logger, c log.Palette) {
	if req.BriefingPath != "" || req.BriefingBody != "" {
		fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	}
	if req.CodexPlanMode {
		fmt.Fprintf(lg.Stdout(), "  %scodex plan mode%s: app-server Plan turn + interactive Codex TUI\n", c.Dim, c.Reset)
	}
	if req.Worktree.Refresh {
		details := req.Worktree.RefreshDetails
		fmt.Fprintf(lg.Stdout(), "    %s$ git -C %s fetch --quiet --no-tags origin %s%s\n", c.Dim, shellQuote(req.Worktree.ProjectRoot), shellQuote(details.FetchBranch), c.Reset)
		if details.LocalBranch != "" {
			fmt.Fprintf(lg.Stdout(), "    %s# may fast-forward the local base before worktree creation%s\n", c.Dim, c.Reset)
			fmt.Fprintf(lg.Stdout(), "    %s$ git -C %s branch -f %s %s%s\n", c.Dim, shellQuote(req.Worktree.ProjectRoot), shellQuote(details.LocalBranch), shellQuote(details.OriginRef), c.Reset)
			fmt.Fprintf(lg.Stdout(), "    %s# if the base is checked out elsewhere, fanout uses merge --ff-only in that worktree%s\n", c.Dim, c.Reset)
		}
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
	if target != "" {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-layout -t %s tiled%s\n", c.Dim, shellQuote(target), c.Reset)
	} else {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-layout tiled%s\n", c.Dim, c.Reset)
	}
	if req.CodexPlanMode {
		fmt.Fprintf(lg.Stdout(), "    %s# fanout waits for app-server Plan turn startup and Codex TUI attach before recording state%s\n", c.Dim, c.Reset)
		fmt.Fprintf(lg.Stdout(), "    %s# status file: %s%s\n", c.Dim, shellQuote(req.CodexPlanStatusPath), c.Reset)
	}
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/state.json with paneId <pane_id>%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/worktree-metadata.json in the child worktree%s\n", c.Dim, c.Reset)
	lg.Ok("%s: dry-run complete", paneLogLabel(req))
}

func paneTitle(req paneRequest) string {
	if req.DisplayNameOverride != "" {
		return req.DisplayNameOverride
	}
	return req.Slug
}

func paneLogLabel(req paneRequest) string {
	if req.TaskID != "" {
		return req.TaskID
	}
	return fmt.Sprintf("#%d", req.Number)
}

func rollbackState(recorder paneStateRecorder, req paneRequest, lg *log.Logger) {
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

func cleanupFailedLaunch(label string, paneID string, prepared worktree.Result, lg *log.Logger) {
	if paneID != "" {
		if err := tmuxrun.KillPane(paneID); err != nil {
			lg.Warn("%s: cleanup incomplete pane %s: %v", label, paneID, err)
		}
	}
	if err := worktree.CleanupCreated(prepared); err != nil {
		lg.Warn("%s: cleanup incomplete worktree %s: %v", label, prepared.WorktreePath, err)
	}
}
