package main

import (
	"fmt"
	"os"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/naming"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	"github.com/butaosuinu/fanout/internal/settings"
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

func createPaneForIssue(cfg *cliflags.Config, lg *log.Logger, info *fanoutruntime.Info, issue ghissue.Issue, resolvedSettings settings.Settings, c log.Palette) bool {
	req := newPaneRequest(cfg, info.ProjectRoot, issue, resolvedSettings)
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

	paneID, err := tmuxrun.SplitPane(info.Target, prepared.WorktreePath)
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
	if err := tmuxrun.SendShellCommand(paneID, req.AgentCommand); err != nil {
		lg.Err("#%d: %v", req.Issue.Number, err)
		cleanupFailedLaunch(req.Issue.Number, paneID, prepared.Plan, lg)
		return false
	}
	lg.Ok("#%d: pane %s created in %s", req.Issue.Number, paneID, prepared.WorktreePath)
	return true
}

func buildAgentCommand(cfg *cliflags.Config, prompt string) (string, error) {
	if cfg.DryRun {
		return agent.BuildCommand(cfg.Agent, prompt)
	}
	return agent.BuildResolvedCommand(cfg.Agent, prompt)
}

func newPaneRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue, resolvedSettings settings.Settings) paneRequest {
	slug := naming.Slug(issue.Title, issue.Number)
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
		}
		req.DisplayNameOverride = name.DisplayName
		req.BranchName = naming.BranchName(name.BranchName, cfg.BranchPrefix, req.Slug)
	} else {
		req.BranchName = naming.BranchName("", cfg.BranchPrefix, req.Slug)
	}
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
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux split-window -t %s -d -h -P -F '#{pane_id}' -c %s%s\n", c.Dim, shellQuote(target), shellQuote(req.Worktree.WorktreePath), c.Reset)
	} else {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux split-window -d -h -P -F '#{pane_id}' -c %s%s\n", c.Dim, shellQuote(req.Worktree.WorktreePath), c.Reset)
	}
	if title := paneTitle(req); title != "" {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-pane -t <pane_id> -T %s%s\n", c.Dim, shellQuote(title), c.Reset)
	}
	if target != "" {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-layout -t %s tiled%s\n", c.Dim, shellQuote(target), c.Reset)
	} else {
		fmt.Fprintf(lg.Stdout(), "    %s$ tmux select-layout tiled%s\n", c.Dim, c.Reset)
	}
	fmt.Fprintf(lg.Stdout(), "    %s$ tmux send-keys -t <pane_id> %s Enter%s\n", c.Dim, shellQuote(req.AgentCommand), c.Reset)
	lg.Ok("#%d: dry-run complete", req.Issue.Number)
}

func paneTitle(req paneRequest) string {
	if req.DisplayNameOverride != "" {
		return req.DisplayNameOverride
	}
	return req.Slug
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
