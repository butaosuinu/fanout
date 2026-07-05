package main

import (
	"fmt"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

func validateIssueAgents(cfg *cliflags.Config, issues, limitDeferred []ghissue.Issue) error {
	targets := make([]agentTarget, 0, len(issues)+len(limitDeferred))
	for _, issue := range issues {
		targets = append(targets, agentTarget{
			Label:            fmt.Sprintf("#%d", issue.Number),
			Target:           fmt.Sprintf("%d", issue.Number),
			Name:             cfg.EffectiveAgentForIssue(issue.Number),
			RequireInstalled: true,
		})
	}
	for _, issue := range limitDeferred {
		targets = append(targets, agentTarget{
			Label:  fmt.Sprintf("#%d", issue.Number),
			Target: fmt.Sprintf("%d", issue.Number),
			Name:   cfg.EffectiveAgentForIssue(issue.Number),
		})
	}
	return validateAgentTargets(cfg, targets)
}

func validateTaskAgents(cfg *cliflags.Config, tasks, limitDeferred []planspec.Task) error {
	targets := make([]agentTarget, 0, len(tasks)+len(limitDeferred))
	for _, task := range tasks {
		targets = append(targets, agentTarget{
			Label:            task.ID,
			Target:           task.ID,
			Name:             cfg.EffectiveAgent(task.ID),
			RequireInstalled: true,
		})
	}
	for _, task := range limitDeferred {
		targets = append(targets, agentTarget{
			Label:  task.ID,
			Target: task.ID,
			Name:   cfg.EffectiveAgent(task.ID),
		})
	}
	return validateAgentTargets(cfg, targets)
}

type agentTarget struct {
	Label            string
	Target           string
	Name             string
	RequireInstalled bool
}

func validateAgentTargets(cfg *cliflags.Config, targets []agentTarget) error {
	seen := map[string]bool{}
	for _, target := range targets {
		if target.Name == "" {
			return fmt.Errorf("%s: agent is required; pass --agent <name>, --agent %s=<name>, or set FANOUT_AGENT", target.Label, target.Target)
		}
		if cfg.CodexPlanModeEnabled() && target.Name != "codex" {
			return fmt.Errorf("--codex-plan-mode requires --agent codex")
		}
		if seen[target.Name] {
			continue
		}
		seen[target.Name] = true
		if err := agent.ValidateKnown(target.Name); err != nil {
			return err
		}
		if target.RequireInstalled && !cfg.DryRun {
			if err := agent.ValidateInstalled(target.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
