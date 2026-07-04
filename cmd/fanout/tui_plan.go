package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/planspec"
	fanoutruntime "github.com/butaosuinu/fanout/internal/runtime"
	fanouttui "github.com/butaosuinu/fanout/internal/tui"
)

func newTUIListPlanSlugsFunc(projectRoot string) func() ([]string, error) {
	return func() ([]string, error) {
		return listPlanSlugs(projectRoot)
	}
}

// newTUIListPlanTasksFunc loads a stored plan spec and returns its task rows
// for the agent-assignment step. Completion is not computed here: it costs
// one `gh pr list` per task, and an override for a completed task is
// harmless.
func newTUIListPlanTasksFunc(projectRoot string) func(slug string) ([]fanouttui.PlanTaskItem, error) {
	return func(slug string) ([]fanouttui.PlanTaskItem, error) {
		spec, err := planspec.LoadWithoutResolvedNameChecks(resolvePlanSpecPath(projectRoot, slug))
		if err != nil {
			return nil, err
		}
		items := make([]fanouttui.PlanTaskItem, 0, len(spec.Tasks))
		for _, task := range spec.Tasks {
			items = append(items, fanouttui.PlanTaskItem{ID: task.ID, Title: task.Title, Wave: task.Wave})
		}
		return items, nil
	}
}

func newTUIPlanLaunchFunc(projectRoot, session, commandName string) fanouttui.PlanLaunchFunc {
	return func(slug, defaultAgent string, overrides map[string]string) (string, error) {
		return launchPlanFromTUI(projectRoot, session, commandName, slug, defaultAgent, overrides)
	}
}

// launchPlanFromTUI runs the live `fanout plan <slug>` lane against the TUI's
// session, reusing runPlanWithRuntime with a synthesized runtime.
func launchPlanFromTUI(projectRoot, session, commandName, slug, defaultAgent string, overrides map[string]string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", fmt.Errorf("plan slug is required")
	}
	if err := validateTUIAgentSelection(defaultAgent, overrides); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	launchLogger := log.NewWith(&stdout, &stderr, false)
	cfg := planCommandConfig{
		SpecArg:      slug,
		Agent:        defaultAgent,
		SleepBetween: cliflags.DefaultSleepBetween,
		Format:       cliflags.DefaultFormat,
	}
	for target, name := range overrides {
		cfg.AgentOverrides = cliflags.UpsertAgentOverride(cfg.AgentOverrides, target, name)
	}
	rt := &runtimeInfo{
		info: &fanoutruntime.Info{
			Session:     session,
			Target:      tuiLaunchTarget(session),
			ProjectRoot: projectRoot,
		},
		gh: ghissue.Runner{Cwd: projectRoot},
	}
	result, code := runPlanWithRuntime(cfg, rt, launchLogger, commandName)
	if code != exitcode.OK {
		launchErr := bufferedLaunchError(stdout, stderr, "launch plan")
		if result.Created > 0 {
			// The fail-fast loop may have created panes before the failure;
			// they are running agents, so a pure failure report would mislead.
			return "", fmt.Errorf("created %d pane(s), then failed: %w", result.Created, launchErr)
		}
		return "", launchErr
	}
	if result.Created == 0 {
		return fmt.Sprintf("plan %s: nothing to do (all tasks already have a pane or are complete)", slug), nil
	}
	return fmt.Sprintf("plan %s: created %d pane(s)", slug, result.Created), nil
}
