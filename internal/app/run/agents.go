package run

import (
	"fmt"
	"os"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

func resolveDefaultAgent(cfg *cliflags.Config) error {
	if cfg.Agent == "" && os.Getenv("FANOUT_AGENT") != "" {
		return cfg.SetAgent(os.Getenv("FANOUT_AGENT"))
	}
	if cfg.Agent == "" && len(cfg.AgentOverrides) == 0 {
		return fmt.Errorf("agent is required; pass --agent <name> or set FANOUT_AGENT")
	}
	return nil
}

// validateIssueAgents checks the resolved agent for every issue target and
// --limit-deferred issue. Deferred issues are validated for name/known-agent
// but not for install presence.
func validateIssueAgents(cfg *cliflags.Config, issues, limitDeferred []ghissue.Issue) error {
	targets := make([]agentTarget, 0, len(issues)+len(limitDeferred))
	for _, issue := range issues {
		targets = append(targets, agentTarget{
			Label:            fmt.Sprintf("#%d", issue.Number),
			Target:           fmt.Sprintf("%d", issue.Number),
			Selection:        cfg.EffectiveSelection(fmt.Sprint(issue.Number)),
			RequireInstalled: true,
		})
	}
	for _, issue := range limitDeferred {
		targets = append(targets, agentTarget{
			Label:     fmt.Sprintf("#%d", issue.Number),
			Target:    fmt.Sprintf("%d", issue.Number),
			Selection: cfg.EffectiveSelection(fmt.Sprint(issue.Number)),
		})
	}
	return validateAgentTargets(cfg, targets)
}

// validateTaskAgents is the plan-lane variant, keyed by task id.
func validateTaskAgents(cfg *cliflags.Config, tasks, limitDeferred []planspec.Task) error {
	targets := make([]agentTarget, 0, len(tasks)+len(limitDeferred))
	for _, task := range tasks {
		targets = append(targets, agentTarget{
			Label:            task.ID,
			Target:           task.ID,
			Selection:        cfg.EffectiveSelection(task.ID),
			RequireInstalled: true,
		})
	}
	for _, task := range limitDeferred {
		targets = append(targets, agentTarget{
			Label:     task.ID,
			Target:    task.ID,
			Selection: cfg.EffectiveSelection(task.ID),
		})
	}
	return validateAgentTargets(cfg, targets)
}

type agentTarget struct {
	Label            string
	Target           string
	Selection        agent.Selection
	RequireInstalled bool
}

func validateAgentTargets(cfg *cliflags.Config, targets []agentTarget) error {
	seen := map[agent.Selection]bool{}
	for _, target := range targets {
		if target.Selection.Name == "" {
			return fmt.Errorf("%s: agent is required; pass --agent <name>, --agent %s=<name>, or set FANOUT_AGENT", target.Label, target.Target)
		}
		if seen[target.Selection] {
			continue
		}
		seen[target.Selection] = true
		if err := panelaunch.ValidateSelection(target.Selection, cfg.PlanModeEnabled(), cfg.Team); err != nil {
			return err
		}
		if target.RequireInstalled && !cfg.DryRun {
			if err := agent.ValidateInstalled(target.Selection.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
