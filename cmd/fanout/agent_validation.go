package main

import (
	"fmt"

	"github.com/butaosuinu/fanout/internal/agent"
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/planspec"
)

func appendIssues(a, b []ghissue.Issue) []ghissue.Issue {
	if len(b) == 0 {
		return a
	}
	out := make([]ghissue.Issue, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func appendTasks(a, b []planspec.Task) []planspec.Task {
	if len(b) == 0 {
		return a
	}
	out := make([]planspec.Task, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func validateIssueAgents(cfg *cliflags.Config, issues []ghissue.Issue) error {
	targets := make([]agentTarget, 0, len(issues))
	for _, issue := range issues {
		targets = append(targets, agentTarget{
			Label:  fmt.Sprintf("#%d", issue.Number),
			Target: fmt.Sprintf("%d", issue.Number),
			Name:   cfg.EffectiveAgentForIssue(issue.Number),
		})
	}
	return validateAgentTargets(cfg, targets)
}

func validateTaskAgents(cfg *cliflags.Config, tasks []planspec.Task) error {
	targets := make([]agentTarget, 0, len(tasks))
	for _, task := range tasks {
		targets = append(targets, agentTarget{
			Label:  task.ID,
			Target: task.ID,
			Name:   cfg.EffectiveAgent(task.ID),
		})
	}
	return validateAgentTargets(cfg, targets)
}

type agentTarget struct {
	Label  string
	Target string
	Name   string
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
		if !cfg.DryRun {
			if err := agent.ValidateInstalled(target.Name); err != nil {
				return err
			}
		}
	}
	return nil
}
