package main

import (
	"fmt"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/displayname"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
	"github.com/butaosuinu/fanout/internal/state"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
	"github.com/butaosuinu/fanout/internal/worktree"
)

type paneRequest struct {
	Issue               ghissue.Issue
	BriefingPath        string
	BriefingBody        string
	ShortTitle          string
	Slug                string
	DisplayNameOverride string
	BranchName          string
	OneLinePrompt       string
	AgentCommand        string
	Worktree            worktree.Plan
}

type paneStateRecorder interface {
	RecordPane(state.Pane) error
	RemovePane(parent string, issueNum int) error
}

func createPaneForIssue(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, issue ghissue.Issue, resolvedSettings settings.Settings, recorder paneStateRecorder, sharedAcrossParents bool, c log.Palette) bool {
	req := newPaneRequest(cfg, info.ProjectRoot, issue, resolvedSettings, sharedAcrossParents)
	agentCmd, err := buildAgentCommand(cfg, req.OneLinePrompt)
	if err != nil {
		lg.Err("#%d: %v", req.Issue.Number, err)
		return false
	}
	req.AgentCommand = agentCmd
	req.Worktree = worktree.BuildPlan(worktree.Options{
		ProjectRoot: info.ProjectRoot,
		Slug:        req.Slug,
		BranchName:  req.BranchName,
		BaseBranch:  cfg.BaseBranch,
		NoRefresh:   cfg.NoRefresh,
	})
	if req.Worktree.Refresh && req.Worktree.RefreshError != nil {
		lg.Err("#%d: prepare worktree: %v", req.Issue.Number, req.Worktree.RefreshError)
		return false
	}

	if err := os.WriteFile(req.BriefingPath, []byte(req.BriefingBody), 0o644); err != nil {
		lg.Err("#%d: write briefing: %v", req.Issue.Number, err)
		return false
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
		BaseBranch:  cfg.BaseBranch,
		NoRefresh:   cfg.NoRefresh,
	})
	if err != nil {
		lg.Err("#%d: prepare worktree: %v", req.Issue.Number, err)
		return false
	}
	if prepared.AlreadyExists {
		lg.Err("#%d: worktree path already exists during launch: %s (duplicate slug or concurrent fanout run)", req.Issue.Number, prepared.WorktreePath)
		return false
	}

	paneID, err := tmuxrun.SplitPaneWithAgentCommand(info.Target, prepared.WorktreePath, req.AgentCommand)
	if err != nil {
		lg.Err("#%d: %v", req.Issue.Number, err)
		cleanupFailedLaunch(req.Issue.Number, "", prepared.Plan, lg)
		return false
	}
	if err := tmuxrun.SetPaneTitle(paneID, paneTitle(req)); err != nil {
		lg.Warn("#%d: %v", req.Issue.Number, err)
	}
	if err := tmuxrun.SelectTiled(info.Target); err != nil {
		lg.Warn("#%d: %v", req.Issue.Number, err)
	}
	if recorder != nil {
		entry := statePane(cfg, req, paneID, prepared.WorktreePath, time.Now().UTC())
		if err := recorder.RecordPane(entry); err != nil {
			lg.Err("#%d: write fanout state: %v", req.Issue.Number, err)
			cleanupFailedLaunch(req.Issue.Number, paneID, prepared.Plan, lg)
			return false
		}
	}
	if err := displayname.WriteFanoutMetadata(prepared.WorktreePath, displayname.FanoutMetadata{
		Agent:        cfg.Agent,
		DisplayName:  paneTitle(req),
		BranchName:   req.BranchName,
		Slug:         req.Slug,
		WorktreePath: prepared.WorktreePath,
	}); err != nil {
		lg.Err("#%d: write worktree metadata: %v", req.Issue.Number, err)
		rollbackState(recorder, cfg.ParentRef, req.Issue.Number, lg)
		cleanupFailedLaunch(req.Issue.Number, paneID, prepared.Plan, lg)
		return false
	}
	lg.Ok("#%d: pane %s created in %s", req.Issue.Number, paneID, prepared.WorktreePath)
	return true
}

func statePane(cfg *cliflags.Config, req paneRequest, paneID, worktreePath string, now time.Time) state.Pane {
	return state.Pane{
		Parent:       cfg.ParentRef,
		IssueNum:     req.Issue.Number,
		Slug:         req.Slug,
		BranchName:   req.BranchName,
		PaneID:       paneID,
		Agent:        cfg.Agent,
		DisplayName:  paneTitle(req),
		WorktreePath: worktreePath,
		Prompt:       req.OneLinePrompt,
		CreatedAt:    now.Format(time.RFC3339),
	}
}

func buildAgentCommand(cfg *cliflags.Config, prompt string) (string, error) {
	if cfg.DryRun {
		return agent.BuildCommand(cfg.Agent, prompt)
	}
	return agent.BuildResolvedCommand(cfg.Agent, prompt)
}

func newPaneRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue, resolvedSettings settings.Settings, sharedAcrossParents bool) paneRequest {
	slug := naming.Slug(issue.Title, issue.Number)
	slugOverridden := false
	branchOverride := ""
	req := paneRequest{
		Issue:        issue,
		BriefingPath: briefing.Path(projectRoot, issue.Number),
		BriefingBody: briefing.Render(issue.Number, issue.Title, issue.Body, cfg.Agent, resolvedSettings),
		ShortTitle:   shortIssueTitle(issue.Title),
		Slug:         slug,
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
	req.OneLinePrompt = oneLinePrompt(cfg.ParentRef, req)
	return req
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
	return fmt.Sprintf("%s%d of #%s] %s: %s. read %s and begin.", fanoutTagPrefix, req.Issue.Number, parentRef, req.Slug, req.ShortTitle, req.BriefingPath)
}

func logPaneRequest(req paneRequest, lg *log.Logger) {
	lg.Info("#%d: %s", req.Issue.Number, req.ShortTitle)
	lg.Dim("  briefing -> %s", req.BriefingPath)
	lg.Dim("  slug -> %s", req.Slug)
	lg.Dim("  worktree -> %s", req.Worktree.WorktreePath)
	lg.Dim("  branch -> %s", req.BranchName)
	lg.Dim("  base -> %s", req.Worktree.BaseBranch)
	if req.DisplayNameOverride != "" {
		lg.Dim("  display-name -> %s", req.DisplayNameOverride)
	}
}

func printPaneDryRun(req paneRequest, target string, lg *log.Logger, c log.Palette) {
	fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
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
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/state.json with paneId <pane_id>%s\n", c.Dim, c.Reset)
	fmt.Fprintf(lg.Stdout(), "    %s# would write .fanout/worktree-metadata.json in the child worktree%s\n", c.Dim, c.Reset)
	lg.Ok("#%d: dry-run complete", req.Issue.Number)
}

func paneTitle(req paneRequest) string {
	if req.DisplayNameOverride != "" {
		return req.DisplayNameOverride
	}
	return req.Slug
}

func rollbackState(recorder paneStateRecorder, parent string, issueNum int, lg *log.Logger) {
	if recorder == nil {
		return
	}
	if err := recorder.RemovePane(parent, issueNum); err != nil {
		lg.Warn("#%d: could not roll back fanout state: %v", issueNum, err)
	}
}

func cleanupFailedLaunch(issueNum int, paneID string, plan worktree.Plan, lg *log.Logger) {
	if paneID != "" {
		if err := tmuxrun.KillPane(paneID); err != nil {
			lg.Warn("#%d: cleanup incomplete pane %s: %v", issueNum, paneID, err)
		}
	}
	if err := worktree.CleanupCreated(plan); err != nil {
		lg.Warn("#%d: cleanup incomplete worktree %s: %v", issueNum, plan.WorktreePath, err)
	}
}
