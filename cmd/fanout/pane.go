package main

import (
	"fmt"
	"os"
	"time"

	"github.com/butaosuinu/fanout/internal/briefing"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/dmuxconfig"
	"github.com/butaosuinu/fanout/internal/dmuxsession"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/popup"
	"github.com/butaosuinu/fanout/internal/tmuxctl"
)

type paneRequest struct {
	Issue               ghissue.Issue
	BriefingPath        string
	BriefingBody        string
	ShortTitle          string
	SlugHint            string
	DisplayNameOverride string
	OneLinePrompt       string
}

func createPaneForIssue(cfg *cliflags.Config, lg *log.Logger, info *dmuxsession.Info, issue ghissue.Issue, c log.Palette) bool {
	req := newPaneRequest(cfg, info.ProjectRoot, issue)
	if !cfg.DryRun {
		if err := os.WriteFile(req.BriefingPath, []byte(req.BriefingBody), 0o644); err != nil {
			lg.Err("#%d: write briefing: %v", req.Issue.Number, err)
			return false
		}
	}

	logPaneRequest(req, lg)

	baseline, err := currentPanesLen(info.ConfigPath)
	if err != nil {
		lg.Err("#%d: reload dmux config: %v", req.Issue.Number, err)
		return false
	}

	newpanePayload, agentPayload, err := panePayloads(cfg, req.OneLinePrompt)
	if err != nil {
		lg.Err("#%d: %v", req.Issue.Number, err)
		return false
	}

	if cfg.DryRun {
		printPaneDryRun(req, baseline, newpanePayload, agentPayload, info.ControlPane, lg, c)
		return true
	}

	if len(agentPayload) == 0 {
		lg.Err("#%d: no agent resolved. dmux v5.6.3 always shows the agent-choice popup after the prompt popup, so fanout needs an agent name. Pass --agent <name> or invoke fanout from a dmux-managed pane.", req.Issue.Number)
		return false
	}

	return drivePaneCreation(cfg, lg, info, req.Issue.Number, baseline, newpanePayload, agentPayload)
}

func newPaneRequest(cfg *cliflags.Config, projectRoot string, issue ghissue.Issue) paneRequest {
	req := paneRequest{
		Issue:        issue,
		BriefingPath: briefing.Path(projectRoot, issue.Number),
		BriefingBody: briefing.Render(issue.Number, issue.Title, issue.Body),
		ShortTitle:   shortIssueTitle(issue.Title),
	}
	if name := cfg.FindName(issue.Number); name != nil {
		req.SlugHint = name.SlugHint
		req.DisplayNameOverride = name.DisplayName
	}
	req.OneLinePrompt = oneLinePrompt(req)
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

func oneLinePrompt(req paneRequest) string {
	if req.SlugHint != "" {
		return fmt.Sprintf("%s%d] %s: %s. read %s and begin.", fanoutTagPrefix, req.Issue.Number, req.SlugHint, req.ShortTitle, req.BriefingPath)
	}
	return fmt.Sprintf("%s%d] %s: read %s and begin.", fanoutTagPrefix, req.Issue.Number, req.ShortTitle, req.BriefingPath)
}

func logPaneRequest(req paneRequest, lg *log.Logger) {
	lg.Info("#%d: %s", req.Issue.Number, req.ShortTitle)
	lg.Dim("  briefing -> %s", req.BriefingPath)
	if req.SlugHint != "" {
		lg.Dim("  slug-hint -> %s", req.SlugHint)
	}
	if req.DisplayNameOverride != "" {
		lg.Dim("  display-name -> %s", req.DisplayNameOverride)
	}
}

func currentPanesLen(configPath string) (int, error) {
	// Re-read dmux.config.json each iteration: a previous iteration in this
	// run may have already grown panes[], so a cached baseline would let
	// waitForNewPane() return immediately for subsequent issues even if the
	// current pane creation actually failed.
	cfg, err := dmuxconfig.Load(configPath)
	if err != nil {
		return 0, err
	}
	return cfg.PanesLen(), nil
}

func panePayloads(cfg *cliflags.Config, prompt string) (newpanePayload, agentPayload []byte, err error) {
	newpanePayload, err = popup.MakeNewPanePayload(prompt)
	if err != nil {
		return nil, nil, fmt.Errorf("build newPane payload: %w", err)
	}
	if cfg.Agent == "" {
		return newpanePayload, nil, nil
	}
	agentPayload, err = popup.MakeAgentPayload(cfg.Agent)
	if err != nil {
		return nil, nil, fmt.Errorf("build agent payload: %w", err)
	}
	return newpanePayload, agentPayload, nil
}

func printPaneDryRun(req paneRequest, baseline int, newpanePayload, agentPayload []byte, controlPane string, lg *log.Logger, c log.Palette) {
	fmt.Fprintf(lg.Stdout(), "  %sbriefing size%s: %d bytes\n", c.Dim, c.Reset, len(req.BriefingBody))
	fmt.Fprintf(lg.Stdout(), "  %scurrent panes[] length%s: %d\n", c.Dim, c.Reset, baseline)
	tmuxctl.PrintSendKeys(lg.Stdout(), c.Dim, c.Reset, controlPane, "Escape")
	tmuxctl.PrintSendKeys(lg.Stdout(), c.Dim, c.Reset, controlPane, "n")
	fmt.Fprintf(lg.Stdout(), "    %s# would intercept newPanePopup and write: %s%s\n", c.Dim, string(newpanePayload), c.Reset)
	if len(agentPayload) > 0 {
		fmt.Fprintf(lg.Stdout(), "    %s# would intercept agentChoicePopup and write: %s%s\n", c.Dim, string(agentPayload), c.Reset)
	} else {
		fmt.Fprintf(lg.Stdout(), "    %s# WOULD FAIL: agent is empty (pass --agent or run from a dmux pane)%s\n", c.Warn, c.Reset)
	}
	lg.Ok("#%d: dry-run complete", req.Issue.Number)
}

func drivePaneCreation(cfg *cliflags.Config, lg *log.Logger, info *dmuxsession.Info, issueNum, baseline int, newpanePayload, agentPayload []byte) bool {
	pidBaseline, err := popup.BaselinePIDs()
	if err != nil {
		lg.Err("#%d: pgrep baseline: %v", issueNum, err)
		return false
	}

	if err := tmuxctl.SendKeys(info.ControlPane, "Escape"); err != nil {
		lg.Err("#%d: tmux send-keys Escape: %v", issueNum, err)
		return false
	}
	time.Sleep(200 * time.Millisecond)
	if err := tmuxctl.SendKeys(info.ControlPane, "n"); err != nil {
		lg.Err("#%d: tmux send-keys n: %v", issueNum, err)
		return false
	}

	popupTimeout := time.Duration(cfg.PopupTimeoutSec) * time.Second
	if err := popup.InterceptWithDebug(popup.NewPanePattern, pidBaseline, newpanePayload, "  newPanePopup", popupTimeout, lg.Debug); err != nil {
		lg.Err("#%d: %v", issueNum, err)
		return false
	}

	pidBaseline, _ = popup.BaselinePIDs()
	if err := popup.InterceptWithDebug(popup.AgentChoicePattern, pidBaseline, agentPayload, "  agentChoicePopup", popupTimeout, lg.Debug); err != nil {
		lg.Err("#%d: %v", issueNum, err)
		return false
	}

	if !waitForNewPane(info.ConfigPath, baseline, 60*time.Second) {
		lg.Err("#%d: timed out after 60s waiting for config.json to grow", issueNum)
		return false
	}
	current, _ := dmuxconfig.Load(info.ConfigPath)
	cur := 0
	if current != nil {
		cur = current.PanesLen()
	}
	lg.Ok("#%d: pane created (panes[] now %d)", issueNum, cur)
	return true
}

func waitForNewPane(configPath string, baseline int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cfg, err := dmuxconfig.Load(configPath)
		if err == nil && cfg.PanesLen() > baseline {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}
